package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestDistributedBackupAdmissionOnlyCoordinatesWriterStops(t *testing.T) {
	j, p := testVisualJournal(t)
	p.Specification.Placement = &domain.Placement{IngressServerID: p.ServerID, Applications: []domain.ApplicationPlacement{{Targets: []domain.PlacementTarget{{ServerID: uuid.New(), Count: 1}}}}}
	p = p.WithHash()
	if err := j.saveUnit(unitState{Plan: p}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Journal: j}
	for _, action := range []string{"install", "disable", "check", "restore", "backup"} {
		t.Run(action, func(t *testing.T) {
			command := backup.Command{Action: action, Installation: backup.Installation{Policy: backup.Policy{Plan: p, Method: backup.QuiescedFiles}}}
			raw, _ := stablejson.Marshal(command)
			message := jsontext.Value(raw)
			err := e.distributedRuntimeAdmission(api.ExecutionCommand{Kind: "backup", Backup: &message})
			if (err != nil) != (action == "backup") {
				t.Fatalf("action %s parent admission: %v", action, err)
			}
			command.Installation.Policy.Plan.ID = uuid.New()
			command.Installation.Policy.Plan = command.Installation.Policy.Plan.WithHash()
			raw, _ = stablejson.Marshal(command)
			message = jsontext.Value(raw)
			if err = e.distributedRuntimeAdmission(api.ExecutionCommand{Kind: "backup", Backup: &message}); err == nil {
				t.Fatal("stale source policy bypassed renewed approval")
			}
		})
	}
}

func TestCrossHostReservationSurvivesChildAcknowledgementAndRestart(t *testing.T) {
	j, p := testVisualJournal(t)
	parent := uuid.New()
	p.Local = &deployment.LocalPlacement{GroupID: uuid.New()}
	p = p.WithHash()
	unit := deployment.DistributedUnit{Plan: p}
	command := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("a", 64), Phase: "reserve", Unit: unit}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Generation: 1, Kind: "crosshost", ParentId: &parent, PlanHash: command.Digest()}
	raw, _ := stablejson.Marshal(command)
	message := jsontext.Value(raw)
	c.Crosshost = &message
	if _, err := j.Accept(c); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveCrossHost(c, command); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Journal: j}
	if err := engine.finishCrossHost(c, command, deployment.Observation{}, "reserved", false); err != nil {
		t.Fatal(err)
	}
	record, _ := j.Record(c.Id)
	if err := j.Acknowledge(c.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	pending, err := j.Pending()
	if err != nil || len(pending) != 0 {
		t.Fatal("legacy uncertainty sentinel leaked into current transport", err)
	}
	ordinary := visualCommand(p, "observe", 2)
	if _, err = j.Accept(ordinary); err == nil {
		t.Fatal("ordinary command entered between coordinated phases")
	}
	dir := j.dir
	j.Close()
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	engine.Journal = j
	policy := backup.Policy{ID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, Generation: 1, Enabled: true}
	if err = j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy}, Writers: []string{"unconfirmed-remote-writer"}}); err != nil {
		t.Fatal(err)
	}
	// A nil Docker client would fail if tick resumed the saved writer before
	// checking the reservation. No disconnected schedule effect may start.
	if err = engine.TickSchedules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = j.RequireUnreservedHost(); err == nil {
		t.Fatal("local update admission ignored reservation")
	}
	if err = engine.ResolveScheduledRun(context.Background(), policy.ID, uuid.New()); err == nil {
		t.Fatal("local reconciliation resumed a writer during reservation")
	}
	saved, err := j.schedule(policy.ID)
	if err != nil || len(saved.Writers) != 1 {
		t.Fatal("reservation resumed a saved writer", err)
	}
	if err = j.Transfer("https://new.example.test", make([]byte, ed25519.PublicKeySize), 1, strings.Repeat("t", 43)); err == nil {
		t.Fatal("controller transfer bypassed the host reservation")
	}
	next := c
	next.Id = uuid.New()
	next.Generation = 2
	command.Phase = "prepare"
	raw, _ = stablejson.Marshal(command)
	message = jsontext.Value(raw)
	next.Crosshost = &message
	next.PlanHash = command.Digest()
	if _, err = j.Accept(next); err != nil {
		t.Fatal("same parent could not resume after restart", err)
	}
	if err = engine.finishCrossHost(next, command, deployment.Observation{}, "complete", true); err != nil {
		t.Fatal(err)
	}
	record, _ = j.Record(next.Id)
	if err = j.Acknowledge(next.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	ordinary.Generation = 3
	if _, err = j.Accept(ordinary); err != nil {
		t.Fatal("confirmed completion did not release ordinary admission", err)
	}
}

// Management can release a secondary held in preparation, so one host's failure doesn't wait on an
// operator while the rest stay reserved. A primary, or a failure nobody cancelled, still waits.
func TestCancellationEndsAHeldPreparation(t *testing.T) {
	for name, primary := range map[string]bool{"secondary": false, "primary": true} {
		t.Run(name, func(t *testing.T) {
			j, p := testVisualJournal(t)
			p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: primary}
			p = p.WithHash()
			parent := uuid.New()
			body := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("a", 64), Phase: "prepare", Unit: deployment.DistributedUnit{Plan: p}}
			raw, _ := stablejson.Marshal(body)
			message := jsontext.Value(raw)
			c := api.ExecutionCommand{Id: uuid.New(), ParentId: &parent, ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Kind: "crosshost", Action: "inspect", Generation: 1, PlanHash: body.Digest(), Crosshost: &message}
			if _, err := j.Accept(c); err != nil {
				t.Fatal(err)
			}
			e := &Engine{Journal: j}
			if err := e.finish(c.Id, "needs_attention", "A private binding is unreachable from its assigned unit"); err != nil {
				t.Fatal(err)
			}
			held, err := j.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if heldPreparationCancelled(held) {
				t.Fatal("a failure nobody cancelled was released")
			}
			// As the poll delivers it. The client's pending loop then picks up what this selects.
			c.CancelRequested = true
			if _, err = j.Accept(c); err != nil {
				t.Fatal(err)
			}
			pending, err := j.Pending()
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending %d %v", len(pending), err)
			}
			if released := heldPreparationCancelled(pending[0]); released == primary {
				t.Fatalf("release of a held %s = %v", name, released)
			}
			if !primary {
				if err = e.Execute(context.Background(), pending[0].Command); err != nil {
					t.Fatal(err)
				}
				if r, _ := j.Record(c.Id); r.Report.State != "cancelled" {
					t.Fatalf("a cancelled preparation stayed held: %s", r.Report.State)
				}
			}
		})
	}
}
