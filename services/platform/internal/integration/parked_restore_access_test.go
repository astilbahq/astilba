package integration

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/moby/moby/client"
)

// PLAT-448. A restore parked for attention keeps no repository access in the agent's journal. When
// the operator retries it, the agent asks management for the access again; management releases the
// policy's access again, through the broker, and the restore goes on with it. This server runs
// nothing a restore could check against, so it then stops at its own checks, which it reaches only
// past the wait for its access.
func TestParkedRestoreGivesUpItsAccessAndARetryGetsItBack(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	// The test takes the new primary's commands through its own journal and, later, its agent.
	g.pause(l.b, true)
	journal := g.journaled(t, l.b)
	got := g.recover(l, l.policy, recoveryRequest(l.snapshot))
	var op api.Operation
	if got.err != nil || got.status != http.StatusAccepted || json.Unmarshal(got.body, &op) != nil {
		t.Fatalf("restore = %d %s %v", got.status, got.body, got.err)
	}
	restore, err := g.pollUntil(t, l.b)
	if err != nil || restore == nil || restore.Id != op.Id || restore.SecretRelease == nil || !restore.SecretRelease.Values {
		t.Fatal("the restore was not released with its access", err)
	}
	holds := func() bool {
		t.Helper()
		r, err := journal.Record(op.Id)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := stablejson.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(raw), "fixture-repository-password") || strings.Contains(string(raw), "storage-secret")
	}
	if _, err = journal.Accept(*restore); err != nil || !holds() {
		t.Fatal("the journal did not take the restore's access", err)
	}
	if err = journal.Mutate(op.Id, func(r *agent.Record) {
		r.Report.State, r.Report.Note = "needs_attention", "An interrupted local schedule requires inspection and resolve-run before another host operation."
	}); err != nil {
		t.Fatal(err)
	}
	parked, err := journal.Record(op.Id)
	if err != nil || holds() {
		t.Fatal("a parked restore kept its repository access", err)
	}
	if err = g.f.execution.Report(g.ctx, l.b.identity, parked.Report); err != nil {
		t.Fatal(err)
	}
	if state, _ := g.operationState(t, op.Id); state != "needs_attention" {
		t.Fatal("management did not record the restore as parked:", state)
	}
	releases := func() int {
		t.Helper()
		var n int
		if err := g.f.st.Pool.QueryRow(g.ctx, "SELECT count(*) FROM secret_access WHERE operation_id=$1 AND purpose='deliver' AND reason='redelivery'", op.Id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if releases() != 0 {
		t.Fatal("the access was released again before anything asked for it")
	}

	// The operator retries it, and the agent runs.
	if err = journal.Retry(op.Id); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(g.ctx)
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker})
	host.HTTP = &http.Client{Transport: g.f.server.Client().Transport}
	g.mu.Lock()
	heartbeat := api.AgentHeartbeat{Capabilities: l.b.input.Capabilities, Preflight: l.b.input.Preflight}
	g.mu.Unlock()
	host.Observe = func(context.Context) api.AgentHeartbeat { return heartbeat }
	stopped := make(chan error, 1)
	go func() { stopped <- host.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = docker.Close()
	})
	var ended agent.Record
	g.waitFor(t, "the retried restore to end", func() bool {
		ended, err = journal.Record(op.Id)
		return err == nil && ended.Report.State != "running" && ended.Report.State != "needs_attention"
	})
	// It began work, which it does only once it holds its access again.
	if ended.StartedAt == nil || ended.Report.State != "failed" {
		t.Fatalf("the retried restore did not go on with its access: %s %q", ended.Report.State, ended.Report.Note)
	}
	if releases() != 1 {
		t.Fatal("the retried restore's access was not released again through the broker exactly once:", releases())
	}
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatal("management did not record the retried restore's outcome:", state)
	}
}
