package agent

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/client"
)

// deploymentCommand freezes a deployment phase that redeploys the writer's plan unchanged, so the
// running writer is both its candidate and its old pool, and no phase may remove it.
func (w *unreadyWriter) deploymentCommand(phase string) api.ExecutionCommand {
	return w.freeze(deployment.CrossHostCommand{ParentID: w.parent, GroupHash: w.root, Phase: phase, Unit: deployment.DistributedUnit{Plan: w.plan, Previous: w.plan}})
}

// reserveDeployment installs the deployment's reservation as its reserve phase did, and moves it
// on to phase, as the phases since did.
func (w *unreadyWriter) reserveDeployment(phase string, published bool) {
	w.t.Helper()
	c := w.deploymentCommand("reserve")
	body, err := crossHostCommand(c)
	if err != nil {
		w.t.Fatal(err)
	}
	state, err := w.journal.reserveCrossHost(c, body)
	if err != nil {
		w.t.Fatal(err)
	}
	state.Phase, state.Publication = phase, published
	if published {
		state.DrainUntil = time.Now().UTC()
	}
	if err = w.journal.saveCrossHost(state); err != nil {
		w.t.Fatal(err)
	}
}

// deliverLate delivers a phase first after its admission deadline, and acknowledges the outcome.
func (w *unreadyWriter) deliverLate(phase string, want api.ExecutionState) {
	w.t.Helper()
	c := late(w.deploymentCommand(phase))
	report := w.deliver(c)
	if report.State != want {
		w.t.Fatalf("late %s ended %s: %s", phase, report.State, report.Note)
	}
	if err := w.journal.Acknowledge(c.Id, report.Sequence); err != nil {
		w.t.Fatal(err)
	}
}

func (w *unreadyWriter) containerID() string {
	w.t.Helper()
	v, err := w.docker.ContainerInspect(context.Background(), deployment.UnitName(w.id), client.ContainerInspectOptions{})
	if err != nil {
		w.t.Fatal(err)
	}
	return v.Container.ID
}

// An agent offline while its deployment was published finishes the deployment when it returns.
// Nothing can issue retirement or completion again, so cancelling either would hold the host.
func TestNativeLateRetirementAndCompletionReleaseTheReservation(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	writer := w.containerID()
	w.reserveDeployment("published", true)
	w.deliverLate("retire", "succeeded")
	if !w.barrier() {
		t.Fatal("retirement released the reservation before completion")
	}
	w.deliverLate("complete", "succeeded")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() || w.containerID() != writer {
		t.Fatal("the deployment stopped or replaced the pool it published")
	}
}

// A publication that arrives late doesn't start, because its probes are that old. Its parent then
// cleans up, and cleanup that arrives late too still releases the host.
func TestNativeLatePublicationLeavesTheHostToAbort(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	writer := w.containerID()
	w.reserveDeployment("probed", false)
	w.deliverLate("publish", "cancelled")
	if state, err := w.journal.crossHost(); err != nil || state.Phase != "probed" || state.Publication {
		t.Fatal("a late publication changed the reservation", err)
	}
	w.deliverLate("abort", "succeeded")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() || w.containerID() != writer {
		t.Fatal("cleanup removed a container the deployment didn't create")
	}
}
