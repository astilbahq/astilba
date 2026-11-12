package integration

import (
	"encoding/json/v2"
	"fmt"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/portability"

	"github.com/google/uuid"
)

// recoveryBase is a base's row in management's registry of the bases hosts may hold.
type recoveryBase struct {
	parent, environment, primary uuid.UUID
	captured                     time.Time
	closed                       *time.Time
}

func registeredBase(t *testing.T, g *revokedGroup, parent uuid.UUID) (recoveryBase, bool) {
	t.Helper()
	var b recoveryBase
	err := g.f.st.Pool.QueryRow(g.ctx, `SELECT parent_id,environment_id,primary_server_id,captured_at,closed_at FROM recovery_bases WHERE workspace_id=$1 AND parent_id=$2`, g.ws, parent).Scan(&b.parent, &b.environment, &b.primary, &b.captured, &b.closed)
	if err != nil {
		return b, false
	}
	return b, true
}

// A cross-host deployment's base is registered when it is captured, for its root's environment and
// primary, and counts as closed once the primary acknowledges the root's completion, as the agent
// counts it. The deployment records when it committed its runtime and when it ended.
func TestRecoveryBasesAreRegisteredAndClosedByTheirPrimary(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	before := time.Now()
	status := g.reviewed(t, &e, g.place(t, &e, a.id, b.id))
	op := g.apply(t, &e, status)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("the deployment ended %s", state)
	}
	base, found := registeredBase(t, g, op.Id)
	if !found || base.primary != a.id || base.environment != status.Plan.EnvironmentID || base.captured.Before(before.Add(-time.Minute)) {
		t.Fatalf("the base was registered as %+v (found %v)", base, found)
	}
	g.waitFor(t, "the primary's completion to close the base", func() bool {
		base, _ = registeredBase(t, g, op.Id)
		return base.closed != nil
	})
	if base.closed.Before(base.captured) {
		t.Fatal("the base closed before it was captured")
	}
	var recorded, ended *time.Time
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT runtime_recorded_at,ended_at FROM execution_operations WHERE id=$1`, op.Id).Scan(&recorded, &ended); err != nil {
		t.Fatal(err)
	}
	if recorded == nil || ended == nil || recorded.Before(base.captured) || ended.Before(*recorded) {
		t.Fatalf("the deployment recorded its runtime at %v and ended at %v", recorded, ended)
	}
}

// A primary that fails the checkpoint phase may already have kept the capsule, and an agent keeps an
// unclosed capsule for good, so management closes it there too.
func TestFailedCheckpointIsClosedOnItsPrimary(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	status := g.reviewed(t, &e, g.place(t, &e, a.id, b.id))
	g.mu.Lock()
	g.failing["crosshost-checkpoint/"+a.id.String()] = true
	g.mu.Unlock()
	op := g.apply(t, &e, status)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("the deployment ended %s", state)
	}
	g.waitFor(t, "the primary to close the checkpoint", func() bool { return g.received(a, "checkpoint-closed") })
	g.waitFor(t, "the closure to close the base", func() bool {
		base, found := registeredBase(t, g, op.Id)
		return found && base.closed != nil
	})
	var cancelled bool
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT cancel_requested FROM execution_operations WHERE id=$1`, op.Id).Scan(&cancelled); err != nil || cancelled {
		t.Fatal("the closure marked a failed deployment as cancelled", err)
	}
}

// The times recovery chooses its window by are written once: a later write can't move when a
// runtime change committed, or when an operation that stays ended ended. An operation that recovery
// resumes ends again later.
func TestRecoveryHistoryTimesAreWrittenOnce(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	status := g.reviewed(t, &e, g.place(t, &e, a.id, b.id))
	op := g.apply(t, &e, status)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("the deployment ended %s", state)
	}
	times := func() (recorded, ended *time.Time) {
		t.Helper()
		if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT runtime_recorded_at,ended_at FROM execution_operations WHERE id=$1`, op.Id).Scan(&recorded, &ended); err != nil {
			t.Fatal(err)
		}
		return recorded, ended
	}
	recorded, ended := times()
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET runtime_recorded_at=now()+interval '1 day',ended_at=now()+interval '1 day',state='failed' WHERE id=$1`, op.Id); err != nil {
		t.Fatal(err)
	}
	if r, e := times(); !r.Equal(*recorded) || !e.Equal(*ended) {
		t.Fatalf("a later write moved the times from %v, %v to %v, %v", recorded, ended, r, e)
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET state='running' WHERE id=$1`, op.Id); err != nil {
		t.Fatal(err)
	}
	if _, e := times(); e != nil {
		t.Fatal("a resumed operation kept its earlier end", e)
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET state='succeeded' WHERE id=$1`, op.Id); err != nil {
		t.Fatal(err)
	}
	if _, e := times(); e == nil || !e.After(*ended) {
		t.Fatal("a resumed operation didn't record its new end", e)
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET runtime_recorded_at=now() WHERE workspace_id=$1 AND runtime_outcome IS NULL`, g.ws); err == nil {
		t.Fatal("an operation without a runtime outcome took a recording time")
	}
	// A row that arrives already ended, from an import or a recovery, ends no earlier than it
	// arrives, even when it was created long before.
	arrived := uuid.New()
	if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT $2::uuid,workspace_id,server_id,environment_id,(SELECT max(generation)+1 FROM execution_operations WHERE environment_id=o.environment_id),gen_random_uuid(),jsonb_set(command,'{id}',to_jsonb($2::text)),kind,'succeeded',report_sequence,now()-interval '200 days' FROM execution_operations o WHERE id=$1`, op.Id, arrived); err != nil {
		t.Fatal(err)
	}
	var arrivedEnd *time.Time
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT ended_at FROM execution_operations WHERE id=$1`, arrived).Scan(&arrivedEnd); err != nil || arrivedEnd == nil || arrivedEnd.Before(*ended) {
		t.Fatal("a row that arrived ended took an earlier end", arrivedEnd, err)
	}
	// A row that ended before these times were recorded keeps none, so it falls back to its
	// creation time, whatever later writes it.
	tx, err := g.f.st.Pool.Begin(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(g.ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(g.ctx, `UPDATE execution_operations SET ended_at=NULL WHERE id=$1`, arrived); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(g.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET steps='[]',note='pruned' WHERE id=$1`, arrived); err != nil {
		t.Fatal(err)
	}
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT ended_at FROM execution_operations WHERE id=$1`, arrived).Scan(&arrivedEnd); err != nil || arrivedEnd != nil {
		t.Fatal("a later write gave an old operation an end", arrivedEnd, err)
	}
}

// A root that recovery takes over keeps its base on its primary until the primary closes it, so
// the base is registered even though this database never captured it.
func TestRecoveryRegistersTheBaseItTakesOver(t *testing.T) {
	r := newClaimRecovery(t, 7)
	if _, err := r.f.st.Pool.Exec(r.ctx, `DELETE FROM recovery_bases WHERE workspace_id=$1`, r.ws); err != nil {
		t.Fatal(err)
	}
	r.recoverAll(t, r.exports(t, claimExport{}))
	var id, environment, primary uuid.UUID
	var closed *time.Time
	if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT id,environment_id,primary_server_id,closed_at FROM recovery_bases WHERE workspace_id=$1 AND parent_id=$2`, r.ws, r.root.Id).Scan(&id, &environment, &primary, &closed); err != nil {
		t.Fatal("recovery didn't register the base it took over: ", err)
	}
	if id != r.descriptor.BaseID || environment != r.env || primary != r.a.ServerId || closed != nil {
		t.Fatalf("the taken-over base was registered as %s for %s on %s, closed %v", id, environment, primary, closed)
	}
	compressed, err := r.f.st.Keys.Open(r.envelope, fmt.Sprintf("cross-host-recovery:v1:%s:%s:%s:%d:%s", r.ws, r.root.Id, r.descriptor.CheckpointID, 0, r.root.PlanHash))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portability.DecompressRecovery(compressed)
	if err != nil {
		t.Fatal(err)
	}
	var base struct {
		CreatedAt time.Time `json:"createdAt"`
	}
	var captured time.Time
	if err = json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	if err = r.f.st.Pool.QueryRow(r.ctx, `SELECT captured_at FROM recovery_bases WHERE workspace_id=$1 AND id=$2`, r.ws, id).Scan(&captured); err != nil || !captured.Equal(base.CreatedAt) {
		t.Fatalf("the taken-over base was registered as captured at %v, not %v (%v)", captured, base.CreatedAt, err)
	}
}
