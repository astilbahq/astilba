package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// PLAT-388. A backup's pg_dump runs in a helper labelled with its run, sharing the source's
// network namespace, so nothing runs in the source. Stopped while its dump waits on a lock, the
// run fails once its helper is gone, and the dump's backend ends soon after, though the lock is
// still held. Neither the helper's configuration nor its exec shows the database's password.
func nativeStoppedDump(t *testing.T, e *Engine, in backup.Installation, source string, a domain.Application, command func(backup.Command) api.ExecutionCommand) {
	t.Helper()
	ctx := context.Background()
	db := pgClient{container: source}
	inspected, err := e.Docker.ContainerInspect(ctx, source, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	password := ""
	for _, v := range inspected.Container.Config.Env {
		if value, ok := strings.CutPrefix(v, "POSTGRES_PASSWORD="); ok {
			password = value
		}
	}
	if password == "" {
		t.Fatal("the source has no password to look for")
	}
	holding, release := context.WithCancel(ctx)
	defer release()
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = e.pgQuery(holding, db, a, "LOCK TABLE reporting.records IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120)")
	}()
	defer func() {
		_, _ = e.pgQuery(ctx, db, a, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LOCK TABLE reporting.records%'")
		release()
		<-held
	}()
	until := func(what, sql, want string) {
		t.Helper()
		for n := 0; ; n++ {
			out, err := e.pgQuery(ctx, db, a, sql)
			if err == nil && strings.TrimSpace(string(out)) == want {
				return
			}
			if n == 100 {
				t.Fatalf("%s: %q (%v)", what, out, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	until("the lock was never held", "SELECT count(*) FROM pg_locks WHERE relation='reporting.records'::regclass AND mode='AccessExclusiveLock' AND granted", "1")
	run := uuid.New()
	c := command(backup.Command{Action: "backup", Installation: in, RunID: run})
	executed := make(chan error, 1)
	go func() { executed <- e.Execute(ctx, c) }()
	// The dump connects over TCP from the helper, not from inside the source.
	until("the dump never waited on the lock", "SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump' AND wait_event_type='Lock' AND client_addr='127.0.0.1'", "1")
	helper, err := e.Docker.ContainerInspect(ctx, "astilba-dump-"+run.String(), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if helper.Container.Config.Labels["com.astilba.run"] != run.String() || helper.Container.Config.Image != *a.Source.Image || !helper.Container.HostConfig.ReadonlyRootfs || helper.Container.HostConfig.NetworkMode != container.NetworkMode("container:"+source) || helper.Container.HostConfig.Memory != inspected.Container.HostConfig.Memory {
		t.Fatalf("the dump helper is not the run's: %+v", helper.Container.Config)
	}
	// The image's data volume is covered, so the helper leaves no anonymous volume behind.
	for _, m := range helper.Container.Mounts {
		if m.Type == mount.TypeVolume {
			t.Fatalf("the dump helper has a volume at %s", m.Destination)
		}
	}
	views := []string{"/containers/" + helper.Container.ID + "/json"}
	for _, id := range helper.Container.ExecIDs {
		views = append(views, "/exec/"+id+"/json")
	}
	for _, path := range views {
		if raw := dockerView(t, e, path); strings.Contains(string(raw), password) {
			t.Fatal("Docker shows the database password at", path)
		}
	}
	if top, err := e.Docker.ContainerTop(ctx, source, client.ContainerTopOptions{}); err != nil {
		t.Fatal(err)
	} else {
		for _, process := range top.Processes {
			if strings.Contains(strings.Join(process, " "), "pg_dump") {
				t.Fatal("pg_dump runs in the source:", process)
			}
		}
	}
	cancelled := c
	cancelled.CancelRequested = true
	if _, err = e.Journal.Accept(cancelled); err != nil {
		t.Fatal(err)
	}
	if err = <-executed; err != nil {
		t.Fatal(err)
	}
	if r, err := e.Journal.Record(c.Id); err != nil || r.Report.State != "cancelled" {
		t.Fatalf("the stopped dump's command ended %s: %s (%v)", r.Report.State, r.Report.Note, err)
	} else if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	s, err := e.Journal.schedule(in.Policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := s.Runs[len(s.Runs)-1]; last.ID != run || last.State != "failed" {
		t.Fatalf("the stopped dump's run settled as %+v", last)
	}
	if _, err = e.Docker.ContainerInspect(ctx, "astilba-dump-"+run.String(), client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("the stopped dump's helper remains:", err)
	}
	// The lock is still held: the dump's backend ended because its client went away.
	until("the stopped dump's backend remains", "SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump'", "0")
	until("the lock was released early", "SELECT count(*) FROM pg_locks WHERE relation='reporting.records'::regclass AND mode='AccessExclusiveLock' AND granted", "1")
}

// dockerView is Docker's raw JSON for path, as anyone with access to the daemon sees it.
func dockerView(t *testing.T, e *Engine, path string) []byte {
	t.Helper()
	host, err := client.ParseHostURL(e.Docker.DaemonHost())
	if err != nil {
		t.Fatal(err)
	}
	daemon := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, host.Scheme, host.Host)
	}}}
	resp, err := daemon.Get("http://docker" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("Docker refused %s: %d %s (%v)", path, resp.StatusCode, raw, err)
	}
	return raw
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
