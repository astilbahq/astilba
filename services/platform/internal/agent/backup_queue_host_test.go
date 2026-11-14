package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// PLAT-366. pg_dump and psql run in a source container their run doesn't own. Stopped part way,
// pg_dump is ended there, so the run's cleanup is confirmed. A process the agent can't end leaves
// the run to inspect.
func nativeStoppedSourceExec(t *testing.T, e *Engine, source string, a domain.Application) {
	t.Helper()
	ctx := context.Background()
	stopped, stop := context.WithCancel(ctx)
	err := e.containerExec(stopped, source, cancelOnWrite(stop), "pg_dump", "-Fc", "-Z0", "--no-password", "-U", a.Postgres.Username, "-d", a.Postgres.Database)
	if left := (*processLeft)(nil); err == nil || errors.As(err, &left) {
		t.Fatalf("a stopped dump was not ended in its source: %v", err)
	}
	for n := 0; ; n++ {
		out, err := e.pgQuery(ctx, source, a, "SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump'")
		if err == nil && strings.TrimSpace(string(out)) == "0" {
			break
		}
		if n == 50 {
			t.Fatalf("the stopped dump is still connected: %s (%v)", out, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// A Docker that refuses every exec after the first, so the agent can't signal the process.
	var execs atomic.Int32
	original := e.Docker
	e.Docker = refusingDocker(t, func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec") && execs.Add(1) > 1
	})
	stopped, stop = context.WithCancel(ctx)
	err = e.containerExec(stopped, source, cancelOnWrite(stop), "sh", "-c", "echo started && exec sleep 30")
	e.Docker = original
	if left := (*processLeft)(nil); !errors.As(err, &left) {
		t.Fatalf("a process the agent couldn't end was taken as ended: %v", err)
	}
}

// cancelOnWrite stops its command once the command has written something.
type cancelOnWrite context.CancelFunc

func (c cancelOnWrite) Write(b []byte) (int, error) {
	c()
	return len(b), nil
}

// PLAT-369. A run-now backup that waits for the host behind another policy's long scheduled run
// is taken up before its deadline and runs past it, bounded by its lifetime from when the host
// took it up rather than from when the client accepted it. One that
// reaches the host after its deadline, as when its agent was silent, never starts.
func nativeQueuedRunNow(t *testing.T, e *Engine, in backup.Installation, command func(backup.Command) api.ExecutionCommand, deliver func(api.ExecutionCommand) api.AgentReport) {
	t.Helper()
	ctx := context.Background()
	// Another policy's scheduled run is the long run. This policy's own due occurrence already ran.
	long := in
	long.Policy.ID, long.Policy.Name = uuid.New(), "Long scheduled run"
	if r := deliver(command(backup.Command{Action: "install", Installation: long})); r.State != "succeeded" {
		t.Fatalf("installing the long policy ended %s: %s", r.State, r.Note)
	}
	// Commands are issued in order, so the run-now is issued after the install: a host that took a
	// later command for the same environment never runs an earlier one.
	queued, missed := uuid.New(), uuid.New()
	runNow := command(backup.Command{Action: "backup", Installation: in, RunID: queued})
	runNow.Deadline = time.Now().Add(30 * time.Second)
	var uploads atomic.Int32
	holding := make(chan struct{})
	e.Point = func(point string) {
		if point != "backup-uploading" {
			return
		}
		if uploads.Add(1) == 1 {
			// The scheduled run holds the host until the run-now backup's deadline is close.
			close(holding)
			time.Sleep(time.Until(runNow.Deadline.Add(-10 * time.Second)))
			return
		}
		// The run-now backup is still uploading when its deadline passes.
		time.Sleep(time.Until(runNow.Deadline.Add(2 * time.Second)))
	}
	defer func() { e.Point = nil }()
	s, err := e.Journal.schedule(long.Policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Installation.Next = long.Policy.Schedule.Next(time.Now().UTC().Add(-48 * time.Hour))
	if err = e.Journal.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	ticked := make(chan error, 1)
	go func() { ticked <- e.TickSchedules(ctx) }()
	select {
	case <-holding:
	case err = <-ticked:
		t.Fatalf("the long scheduled run never reached its upload: %v", err)
	}
	// The client accepts the command as it polls, before it waits for the host.
	if _, err = e.Journal.Accept(runNow); err != nil {
		t.Fatal(err)
	}
	if r := deliver(runNow); r.State != "succeeded" || !time.Now().After(runNow.Deadline) {
		t.Fatalf("the queued run-now backup ended %s: %s", r.State, r.Note)
	}
	if err = <-ticked; err != nil {
		t.Fatal(err)
	}
	if s, err = e.Journal.schedule(long.Policy.ID); err != nil || len(s.Runs) != 1 || s.Runs[0].State != "succeeded" {
		t.Fatalf("the long scheduled run did not succeed: %+v (%v)", s.Runs, err)
	}
	s.Installation.Policy.Enabled = false
	if err = e.Journal.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	late := command(backup.Command{Action: "backup", Installation: in, RunID: missed})
	late.Deadline = time.Now().Add(-time.Second)
	if r := deliver(late); r.State != "cancelled" {
		t.Fatalf("a backup delivered after its deadline ended %s: %s", r.State, r.Note)
	}
	if s, err = e.Journal.schedule(in.Policy.ID); err != nil {
		t.Fatal(err)
	}
	ran := map[uuid.UUID]string{}
	for _, r := range s.Runs {
		ran[r.ID] = r.State
	}
	if ran[queued] != "succeeded" {
		t.Fatalf("the queued run-now backup's run is %q", ran[queued])
	}
	if _, started := ran[missed]; started {
		t.Fatal("a backup delivered after its deadline started a run")
	}
}
