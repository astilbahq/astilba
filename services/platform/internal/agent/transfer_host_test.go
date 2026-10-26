package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func nativeControllerTransfer(t *testing.T, e *Engine) *Journal {
	t.Helper()
	ctx := context.Background()
	j := e.Journal
	before, err := j.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	states, err := j.scheduleStates()
	if err != nil {
		t.Fatal(err)
	}
	var job *scheduleState
	for i := range states {
		if states[i].Installation.Policy.Method == "container-job" && states[i].Installation.Policy.Enabled {
			job = &states[i]
			break
		}
	}
	if job == nil {
		t.Fatal("native transfer needs an installed job")
	}
	if err = e.checkBackupSource(ctx, job.Installation.Policy); err != nil {
		t.Fatal("transfer fixture must start with a valid managed schedule source", err)
	}
	app := job.Installation.Policy.ResourceID
	original, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(app), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Transfer("https://127.0.0.1:1", public, 1, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	dir := j.dir
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal = j
	after, err := j.Inventory()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("transfer changed unit/schedule identities: %v", err)
	}
	// Resume the real daemon against an unavailable target. It must still execute
	// a due installed job exactly once while enrollment retries in the background.
	now := time.Now().UTC()
	// A future minute is fresh and cannot cross the missed-job cutoff before
	// the scheduler first ticks, even if the test starts at a minute boundary.
	job.Installation.Next = now.Truncate(time.Minute).Add(time.Minute)
	if err = j.saveSchedule(*job); err != nil {
		t.Fatal(err)
	}
	count := len(job.Runs)
	runCtx, cancel := context.WithCancel(ctx)
	c := NewClient(j, e)
	c.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	deadline := job.Installation.Next.Add(10 * time.Second)
	for {
		state, err := j.schedule(job.Installation.Policy.ID)
		if err == nil && len(state.Runs) == count+1 && state.Runs[len(state.Runs)-1].State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("schedule did not survive transfer while target was unavailable: before=%d after=%+v next=%s due=%s", count, state.Runs, state.Installation.Next, job.Installation.Next)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	actual, err := e.Docker.ContainerInspect(ctx, original.Container.ID, client.ContainerInspectOptions{})
	if err != nil || actual.Container.ID != original.Container.ID || !actual.Container.State.Running {
		t.Fatal("transfer replaced or stopped the workload")
	}
	// Even a numerically later command from the old authority stays rejected.
	unit, err := j.unit(job.Installation.Policy.Plan.UnitID)
	if err != nil {
		t.Fatal(err)
	}
	old := visualCommand(unit.Plan, "observe", 1<<40)
	old.Id = uuid.New()
	if _, err = j.Accept(old); err == nil {
		t.Fatal("old controller command accepted after transfer")
	}
	return j
}
