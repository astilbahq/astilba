package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestUpgradeComparisonPreservesCopyRecords(t *testing.T) {
	preamble := "-- PostgreSQL database dump\n-- Dumped from database version 17.10\n-- Dumped by pg_dump version 18.4\n\nSET statement_timeout = 0;\n"
	payload := "COPY public.records (value) FROM stdin;\n-- Dumped from database version 17.10\nSET not_a_statement\n\\.\n"
	var got bytes.Buffer
	w := upgradeSQLHeader{out: &got}
	for _, b := range []byte(preamble + payload) {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	want := strings.Replace(preamble, "from database version 17.10", "from database version 18.4", 1) + payload
	if got.String() != want || !w.done {
		t.Fatal("archive comparison changed database data")
	}
}

func TestActiveUpgradeHoldsSchedulesUntilItFinishes(t *testing.T) {
	e, p, created := scheduledJobFixture(t, "")
	at := time.Now().UTC()
	tick := func() scheduleState {
		t.Helper()
		if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
		state, err := e.Journal.schedule(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Writers: []string{"source"}}); err != nil {
		t.Fatal(err)
	}
	command := upgrade.Command{Plan: upgrade.Plan{ID: uuid.New()}}
	if err := e.Journal.saveUpgrade(upgradeState{Command: command, Status: upgrade.Status{Phase: "ready"}}); err != nil {
		t.Fatal(err)
	}
	if state := tick(); *created != 0 || len(state.Runs) != 0 || len(state.Writers) != 1 {
		t.Fatal("schedule ran or resumed writers during an active upgrade")
	}
	// Retention outlives the upgrade, but must not hold the host's schedules.
	if err := e.Journal.saveUpgrade(upgradeState{Command: command, Status: upgrade.Status{Phase: "complete", KeepUntil: at.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if state := tick(); *created != 1 || len(state.Runs) != 1 || len(state.Writers) != 0 {
		t.Fatal("finished upgrade kept holding schedules")
	}
}

func deleteVolumeCommand(p deployment.Plan, volume uuid.UUID) api.ExecutionCommand {
	c := visualCommand(p, "delete-volume", 1)
	payload := deployment.Payload{Action: "delete-volume", Plan: p, VolumeID: &volume}
	raw, _ := json.Marshal(payload)
	message := json.RawMessage(raw)
	c.Visual, c.PlanHash = &message, payload.Digest()
	return c
}

// This gate is the agent's only guard on an active upgrade's volumes:
// delete-volume checks retention alone, which an upgrade awaiting recovery can
// outlive. Each allowed command has a refused sibling that differs only in what
// the gate reads.
func TestActiveUpgradeBlocksOtherHostOperations(t *testing.T) {
	const blocked = "An active upgrade blocks other host operations until recovery or cutover completes."
	active, old := uuid.New(), uuid.New()
	upgradeCommand := func(p deployment.Plan, id uuid.UUID) api.ExecutionCommand {
		return api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: id, Kind: "upgrade", Action: "inspect", PlanHash: strings.Repeat("a", 64), Generation: 1, Deadline: time.Now().Add(time.Minute)}
	}
	networkCommand := func(p deployment.Plan, action string) api.ExecutionCommand {
		network, member := uuid.New(), uuid.New()
		manifest := hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: member, ServerID: p.ServerID, Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820}
		body := hostnetwork.Command{Action: action, NetworkID: network, MembershipID: member, Manifest: &manifest}
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		return api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, ResourceId: network, Kind: "network", Action: "inspect", PlanHash: hostnetwork.Hash(body), Generation: 1, Deadline: time.Now().Add(time.Minute), Network: &message}
	}
	// Carries only what the gate reads; executeCrossHost then refuses it as invalid.
	childCommand := func(t *testing.T, j *Journal, p deployment.Plan, maintenance *deployment.Maintenance) api.ExecutionCommand {
		t.Helper()
		parent := uuid.New()
		body := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("b", 64), Phase: "effect", Unit: deployment.DistributedUnit{Plan: p}, Maintenance: maintenance}
		raw, _ := json.Marshal(body)
		message := json.RawMessage(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", PlanHash: body.Digest(), Generation: 1, Deadline: time.Now().Add(time.Minute), Crosshost: &message}
		if _, err := j.reserveCrossHost(c, body); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, tc := range []struct {
		name    string
		command func(*testing.T, *Journal, deployment.Plan) api.ExecutionCommand
		refused bool
	}{
		{"delete_volume_past_retention", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return deleteVolumeCommand(p, old)
		}, true},
		{"deploy", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return visualCommand(p, "deploy", 1)
		}, true},
		{"another_upgrade", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return upgradeCommand(p, uuid.New())
		}, true},
		{"same_upgrade", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return upgradeCommand(p, active)
		}, false},
		{"network_configure", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return networkCommand(p, "configure")
		}, true},
		{"network_restriction", func(_ *testing.T, _ *Journal, p deployment.Plan) api.ExecutionCommand {
			return networkCommand(p, "restrict")
		}, false},
		{"coordinated_deployment", func(t *testing.T, j *Journal, p deployment.Plan) api.ExecutionCommand {
			return childCommand(t, j, p, nil)
		}, true},
		{"another_upgrades_maintenance", func(t *testing.T, j *Journal, p deployment.Plan) api.ExecutionCommand {
			return childCommand(t, j, p, &deployment.Maintenance{Kind: "upgrade", ResourceID: uuid.New()})
		}, true},
		{"same_upgrades_maintenance", func(t *testing.T, j *Journal, p deployment.Plan) api.ExecutionCommand {
			return childCommand(t, j, p, &deployment.Maintenance{Kind: "upgrade", ResourceID: active})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, p := testVisualJournal(t)
			database := uuid.New()
			source := deployment.Plan{Specification: domain.Specification{Applications: []domain.Application{{ID: database, Mounts: []domain.VolumeMount{{VolumeID: old}}}}}}
			plan := upgrade.Plan{ID: active, ResourceID: database, TargetVolumeID: uuid.New(), Source: source}
			status := upgrade.Status{Phase: "attention", KeepUntil: time.Now().Add(-time.Hour)}
			if err := j.saveUpgrade(upgradeState{Command: upgrade.Command{Plan: plan}, Status: status}); err != nil {
				t.Fatal(err)
			}
			c := tc.command(t, j, p)
			if err := (&Engine{Journal: j}).Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if (r.Report.Note == blocked) != tc.refused || tc.refused && r.Report.State != "failed" {
				t.Fatalf("refused = %v, got %s: %s", tc.refused, r.Report.State, r.Report.Note)
			}
		})
	}
}

func TestUpgradeRetentionProtectsVolumes(t *testing.T) {
	old, target := uuid.New(), uuid.New()
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name      string
		volume    uuid.UUID
		status    upgrade.Status
		protected bool
	}{
		{"old_volume_retained", old, upgrade.Status{Phase: "complete", KeepUntil: future}, true},
		{"target_volume_retained", target, upgrade.Status{Phase: "returned", KeepUntil: future}, true},
		{"retention_expired", old, upgrade.Status{Phase: "complete", KeepUntil: past}, false},
		{"unrelated_volume", uuid.New(), upgrade.Status{Phase: "complete", KeepUntil: future}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, p := testVisualJournal(t)
			removals := 0
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/"):
					removals++
					w.WriteHeader(http.StatusNoContent)
				case strings.Contains(r.URL.Path, "/volumes/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": deployment.VolumeName(tc.volume), "Labels": visualLabels(p, tc.volume)})
				case strings.HasSuffix(r.URL.Path, "/json"):
					_, _ = w.Write([]byte("[]"))
				default:
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"absent"}`))
				}
			}))
			defer daemon.Close()
			docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			database := uuid.New()
			source := deployment.Plan{Specification: domain.Specification{Applications: []domain.Application{{ID: database, Mounts: []domain.VolumeMount{{VolumeID: old}}}}}}
			plan := upgrade.Plan{ID: uuid.New(), ResourceID: database, TargetVolumeID: target, Source: source}
			if err = j.saveUpgrade(upgradeState{Command: upgrade.Command{Plan: plan}, Status: tc.status}); err != nil {
				t.Fatal(err)
			}
			c := deleteVolumeCommand(p, tc.volume)
			if err = (&Engine{Journal: j, Docker: docker}).Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			r, err := j.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.protected {
				if removals != 0 || r.Report.State != "failed" {
					t.Fatalf("protected volume was not refused: %d removals, %s: %s", removals, r.Report.State, r.Report.Note)
				}
			} else if removals != 1 {
				t.Fatalf("unprotected volume was refused: %s: %s", r.Report.State, r.Report.Note)
			}
		})
	}
}
