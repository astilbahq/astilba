package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
			c := visualCommand(p, "delete-volume", 1)
			payload := deployment.Payload{Action: "delete-volume", Plan: p, VolumeID: &tc.volume}
			raw, _ := json.Marshal(payload)
			message := json.RawMessage(raw)
			c.Visual, c.PlanHash = &message, payload.Digest()
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
