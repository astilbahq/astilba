package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// late freezes a command past the admission deadline it was first delivered after.
func late(c api.ExecutionCommand) api.ExecutionCommand {
	c.Deadline = time.Now().Add(-time.Hour)
	return c
}

// An agent offline as the parent reaches completion must still release its reservation when
// it returns. Nothing can issue this command again, so cancelling it would strand the host.
func TestNativeLateCompletionReleasesTheReservation(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.phase("reserve")
	w.phase("pause")
	w.phase("resume-source")
	complete := late(w.command("complete"))
	if r := w.deliver(complete); r.State != "succeeded" {
		t.Fatalf("late completion ended %s: %s", r.State, r.Note)
	}
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("late completion stopped the writer")
	}
}

// Lateness is not evidence: completion still has to confirm the writers. Until it can, the
// reservation stays, and the same command finishes once they recover.
func TestNativeLateCompletionKeepsAnUnconfirmedReservation(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.phase("reserve")
	w.phase("pause")
	w.phase("resume-source")
	w.stop()
	complete := late(w.command("complete"))
	if r := w.deliver(complete); r.State != "needs_attention" {
		t.Fatalf("late completion of a stopped writer ended %s: %s", r.State, r.Note)
	}
	if !w.barrier() {
		t.Fatal("a deadline released a reservation whose writers are unconfirmed")
	}
	if _, err := w.docker.ContainerStart(context.Background(), deployment.UnitName(w.id), client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Retry(complete.Id); err != nil {
		t.Fatal(err)
	}
	if r := w.deliver(complete); r.State != "succeeded" {
		t.Fatalf("retried late completion ended %s: %s", r.State, r.Note)
	}
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
}

// A restore replaces data, so unlike resumption it does not start after its deadline. It has
// changed nothing, which is what lets the parent resume the source writers on every host.
func TestNativeLateRestoreLeavesTheSourceToResume(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	target := w.plan
	target.ID = uuid.New()
	target = target.WithHash()
	boundary := deployment.RestoreCutover{SourcePlanID: w.plan.Local.GroupID, SourceHash: strings.Repeat("e", 64), PolicyID: uuid.New(), RunID: uuid.New(), ManifestHash: strings.Repeat("f", 64), OldVolumeID: uuid.New(), TargetVolumeID: uuid.New()}
	w.intent = deployment.Maintenance{Kind: "restore", ResourceID: boundary.PolicyID, RunID: boundary.RunID, Restore: &boundary, EffectHash: deployment.Hash(boundary), ExpectedState: w.intent.ExpectedState, Target: &target}
	// Reserving a restore checks the restored run, which this test has no need of.
	reserve := w.command("reserve")
	body, err := crossHostCommand(reserve)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.journal.reserveCrossHost(reserve, body); err != nil {
		t.Fatal(err)
	}
	w.phase("pause")
	stopped, err := w.docker.ContainerInspect(context.Background(), deployment.UnitName(w.id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restore := late(w.command("restore"))
	report := w.deliver(restore)
	if report.State != "cancelled" {
		t.Fatalf("late restore ended %s: %s", report.State, report.Note)
	}
	if err = w.journal.Acknowledge(restore.Id, report.Sequence); err != nil {
		t.Fatal(err)
	}
	after, err := w.docker.ContainerInspect(context.Background(), deployment.UnitName(w.id), client.ContainerInspectOptions{})
	if err != nil || after.Container.ID != stopped.Container.ID || after.Container.State.Running {
		t.Fatal("a cancelled restore touched the source writer", err)
	}
	if !w.barrier() {
		t.Fatal("a cancelled restore released the reservation")
	}
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("the source writer did not resume")
	}
}
