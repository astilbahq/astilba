package integration

import (
	"bytes"
	"context"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// seedEndedNetworkCommands adds ended network commands for a host, older than anything else in the
// workspace, as a long-lived workspace accumulates them. Each copies a real one.
func seedEndedNetworkCommands(t *testing.T, f *fixture, ws, server uuid.UUID, count int) {
	t.Helper()
	tag, err := f.st.Pool.Exec(context.Background(), `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
SELECT n.id,o.workspace_id,o.server_id,o.environment_id,o.generation,gen_random_uuid(),jsonb_set(o.command,'{id}',to_jsonb(n.id::text)),o.kind,o.state,o.report_sequence,now()-interval '1 day'-n.i*interval '1 second'
FROM (SELECT * FROM execution_operations WHERE workspace_id=$1 AND server_id=$2 AND kind='network' AND state IN ('succeeded','failed','cancelled') ORDER BY created_at LIMIT 1) o
CROSS JOIN (SELECT gen_random_uuid() AS id,i FROM generate_series(1,$3::int) i) n`, ws, server, count)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != int64(count) {
		t.Fatalf("seeded %d ended commands, want %d", tag.RowsAffected(), count)
	}
}

// Operation rows are permanent, and a workspace's recovery snapshot and export used to carry every
// one of them, so a long-lived workspace passed the archive's 10,000 record limit and could no
// longer deploy across hosts or export. They carry the history recovery needs and the history the
// app lists, so both still work, and the imported workspace lists the same operations.
func TestLongOperationHistoryKeepsCrossHostDeploymentAndExport(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	seedEndedNetworkCommands(t, g.f, g.ws, a.id, 10050)

	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	if g.runtimeUnits(t, b.id) != 1 {
		t.Fatal("the cross-host deployment did not place the instance")
	}

	passphrase := "long history archive passphrase"
	var encrypted api.WorkspaceArchive
	g.b.call("POST", g.base+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
	plain, err := portability.Open(g.ctx, passphrase, encrypted.Archive)
	if err != nil {
		t.Fatal(err)
	}
	clear(plain)
	destination := setup(t)
	owner := destination.browser(t)
	owner.login("owner@example.test", destination.password)
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, nil)

	listed := func(b *browser) []uuid.UUID {
		var operations []api.Operation
		b.call("GET", g.base+"/operations", nil, 200, &operations)
		ids := []uuid.UUID{}
		for _, operation := range operations {
			ids = append(ids, operation.Id)
		}
		// Operations started together list in either order.
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		return ids
	}
	if source, imported := listed(g.b), listed(owner); len(source) != 100 || !slices.Equal(source, imported) {
		t.Fatalf("the imported workspace lists %d operations, not the source's %d", len(imported), len(source))
	}
	if kept := claimCounter(t, destination, `SELECT count(*) FROM execution_operations WHERE workspace_id=$1`, g.ws); kept > 1000 {
		t.Fatalf("the export carried %d operations", kept)
	}
}

// A recovery base sealed before snapshots were bounded carries every operation. Recovery still
// reads one, and restores the history it carries to a database that lacks it.
func TestRecoveryReadsABaseCapturedBeforeHistoryWasBounded(t *testing.T) {
	var seeded []uuid.UUID
	r := newClaimRecoveryCaptured(t, 7, func(ctx context.Context, tx pgx.Tx, ws uuid.UUID) ([]byte, error) {
		// What CaptureRecovery wrote before it was bounded: every row of every archived table, in
		// the format of its time.
		archive, _, err := portability.InspectRecovery(ctx, tx, ws)
		if err != nil {
			return nil, err
		}
		archive.Recovery = nil
		return stablejson.Marshal(archive)
	}, func(f *fixture, _ *browser, ws, _ uuid.UUID) {
		server := claimUUID(t, f, `SELECT server_id FROM execution_operations WHERE workspace_id=$1 AND kind='visual' LIMIT 1`, ws)
		network := api.ExecutionCommand{Id: uuid.New(), ServerId: server, Generation: 1, Kind: "network", Action: "inspect", ResourceId: uuid.New(), PlanHash: "seeded", Network: claimMessage(t, map[string]string{"action": "identify"})}
		if _, err := f.st.Pool.Exec(context.Background(), `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,report_sequence,created_at) VALUES($1,$2,$3,1,$4,$5,'network','failed',1,now()-interval '2 days')`, network.Id, ws, server, uuid.New(), claimBytes(t, network)); err != nil {
			t.Fatal(err)
		}
		seeded = append(seeded, network.Id)
		seedEndedNetworkCommands(t, f, ws, server, 300)
		rows, err := f.st.Pool.Query(context.Background(), `SELECT id FROM execution_operations WHERE workspace_id=$1 AND kind='network' AND id<>$2`, ws, network.Id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			seeded = append(seeded, id)
		}
	})
	// The backup being recovered predates that history.
	if _, err := r.f.st.Pool.Exec(r.ctx, `DELETE FROM execution_operations WHERE id=ANY($1)`, seeded); err != nil {
		t.Fatal(err)
	}
	r.recoverAll(t, r.exports(t, claimExport{}))
	if got := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=ANY($1)`, seeded); got != int64(len(seeded)) {
		t.Fatalf("recovery restored %d of the %d operations the base carries", got, len(seeded))
	}
}

// A base's applied history is mostly random identifiers, which compress poorly, so a busy
// workspace's window can fit the archive's limits and still not seal. The deployment then shortens
// the window by how far the envelope overflowed and seals again, rather than failing.
func TestCrossHostDeploymentShortensAWindowThatDoesNotSeal(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	env := uuid.MustParse(path.Base(e.path))
	var plan uuid.UUID
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT o.runtime_plan_id FROM environments e JOIN execution_operations o ON o.id=e.runtime_operation_id WHERE e.id=$1`, env).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	// Each change is a command of its own, recorded inside the window.
	if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,runtime_outcome,runtime_plan_id,created_at,runtime_recorded_at)
SELECT n.id,p.workspace_id,$2,p.environment_id,(SELECT max(generation) FROM execution_operations WHERE environment_id=p.environment_id)+i,gen_random_uuid(),jsonb_build_object('id',n.id,'planHash',md5(random()::text)),
	'visual','succeeded',1,'applied',p.id,now()-interval '30 days'+i*interval '1 second',now()-interval '30 days'+i*interval '1 second'
FROM visual_plans p, (SELECT gen_random_uuid() AS id,i FROM generate_series(1,9500) i) n WHERE p.id=$1`, plan, a.id); err != nil {
		t.Fatal(err)
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE environments SET execution_generation=(SELECT max(generation) FROM execution_operations WHERE environment_id=$1) WHERE id=$1`, env); err != nil {
		t.Fatal(err)
	}
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	var seconds int64
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT extract(epoch FROM captured_at-window_start)::bigint FROM recovery_bases WHERE workspace_id=$1 ORDER BY captured_at DESC LIMIT 1`, g.ws).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if window := time.Duration(seconds) * time.Second; window >= 30*24*time.Hour || window <= 0 {
		t.Fatal("the deployment's base kept a window that couldn't seal: ", window)
	}
}

// A recovery base used to carry every operation that applied a runtime, so a workspace with
// enough deployments reached the archive's record limit and its cross-host deployments stopped. A
// base now carries a window of applied history: changes older than 90 days stay out, and when the
// window's own changes don't fit, the window starts later, so a busy workspace keeps a base.
func TestBusyWorkspaceStaysWithinTheArchiveLimits(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Busy"}, 201, &project)
	var environment api.Environment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &environment)
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
		t.Fatal(err)
	}
	plan := uuid.New()
	unit, resolve := claimUUID(t, f, `SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2`, ws, environment.Id), uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,1,$5,'{}','visual','succeeded',1)`, resolve, ws, in.ServerId, environment.Id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, plan, ws, in.ServerId, environment.Id, unit, environment.DraftRevisionId, resolve); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,'{}')`, plan, ws, environment.Id); err != nil {
		t.Fatal(err)
	}
	// applied seeds count deployments that each applied a runtime and ended age ago, a millisecond
	// apart. The trigger would date their end now, as for a row that arrives ended, so it is off
	// while they are written.
	applied := func(count int, age time.Duration) {
		t.Helper()
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,runtime_outcome,runtime_plan_id,created_at,runtime_recorded_at,ended_at)
SELECT gen_random_uuid(),$1,$2,$3,COALESCE((SELECT max(generation) FROM execution_operations WHERE environment_id=$3),0)+i,gen_random_uuid(),'{}','visual','succeeded',1,'applied',$4,t,t,t
FROM generate_series(1,$5::int) i, LATERAL (SELECT now()-$6::interval+i*interval '1 millisecond' AS t) at`, ws, in.ServerId, environment.Id, plan, count, age.String()); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	capture := func() (portability.RecoveryCapture, portability.Archive) {
		t.Helper()
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		captured, err := portability.CaptureRecoveryBase(ctx, tx, ws, portability.RecoveryWindowTarget)
		if err != nil {
			t.Fatal("the workspace no longer fits a recovery base: ", err)
		}
		archive, err := portability.DecodeRecovery(captured.Raw)
		if err != nil {
			t.Fatal("the recovery base doesn't read back: ", err)
		}
		return captured, archive
	}
	day := 24 * time.Hour
	// A change recorded half a day before the window opens may have committed after it did, so
	// the window counts it, and starts from the one before.
	applied(9000, 120*day)
	applied(1, 100*day)
	applied(1, 90*day+12*time.Hour)
	applied(2000, 10*day)
	captured, archive := capture()
	if window := captured.CapturedAt.Sub(captured.WindowStart); window != portability.RecoveryWindowTarget {
		t.Fatal("a workspace whose history fits got a shorter window: ", window)
	}
	ledger := archive.Recovery.Applied
	if len(ledger) != 2002 || ledger[0].Generation != 9002 || ledger[1].Generation != 9003 {
		t.Fatalf("the base carries %d applied changes from generation %d, not the 2,002 from the change before the window opened", len(ledger), ledger[0].Generation)
	}
	if archive.Recovery.Environments[0].Floor != 9002 || len(archive.Tables["execution_operations"]) > 200 {
		t.Fatal("the base carries applied history as operation rows, or starts it at the wrong change")
	}

	applied(12000, 5*day)
	captured, archive = capture()
	if window := captured.CapturedAt.Sub(captured.WindowStart); window >= 6*day || window <= 0 {
		t.Fatal("a busy workspace's window didn't shrink to the changes that fit: ", window)
	}
	if records := archive.Recovery.Records(); records < 8000 || records > 10000 {
		t.Fatalf("the shortened window holds %d records", records)
	}
	passphrase := "busy workspace archive passphrase"
	var encrypted api.WorkspaceArchive
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
}

// A recovery keeps the applied history it took from a base without the rows, and later bases carry
// it, so a second recovery, from a backup taken inside the first one's gap, still finds that
// backup's runtime. The database's own origin reads it too.
func TestRecoveredHistoryTravelsInLaterBases(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Recovered"}, 201, &project)
	var environment api.Environment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &environment)
	plan := uuid.New()
	kept := portability.AppliedEntry{Environment: environment.Id, Operation: uuid.New(), Generation: 7, Identity: "v2:" + strings.Repeat("ab", 32), Outcome: "applied", RuntimePlan: &plan, State: "succeeded", Sequence: 1, RecordedAt: time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Microsecond), Exact: true}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.StoreRecoveredHistory(ctx, tx, ws, &portability.RecoveryHistory{Applied: []portability.AppliedEntry{kept}}); err != nil {
		t.Fatal(err)
	}
	captured, err := portability.CaptureRecoveryBase(ctx, tx, ws, portability.RecoveryWindowTarget)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := portability.DecodeRecovery(captured.Raw)
	if err != nil {
		t.Fatal(err)
	}
	carries := func(entries []portability.AppliedEntry) bool {
		want, _ := stablejson.Marshal(kept)
		return slices.ContainsFunc(entries, func(e portability.AppliedEntry) bool {
			got, _ := stablejson.Marshal(e)
			return bytes.Equal(got, want)
		})
	}
	if !carries(archive.Recovery.Applied) {
		t.Fatal("a later base left out the applied history a recovery kept without its row")
	}
	inspected, _, err := portability.InspectRecovery(ctx, tx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !carries(inspected.Recovery.Applied) {
		t.Fatal("the database's own applied history left out what a recovery kept")
	}
}
