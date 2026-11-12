package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
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
		// What CaptureRecovery wrote before it was bounded: every row of every archived table.
		archive, _, err := portability.InspectRecovery(ctx, tx, ws)
		if err != nil {
			return nil, err
		}
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

// A recovery base used to be gzipped, and gzip can't find a plan the base repeats further back than
// 32 KiB, so cross-host history filled the 1 MiB envelope long before the archive's record limit,
// after a few weeks in a busy distributed workspace, and its cross-host deployments stopped. Bases
// are now zstd-compressed with a window covering the whole base, which holds about 2.5 times the
// history in the same envelope.
func TestCrossHostHistoryGzipCouldNotSealStillDeploys(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	env := uuid.MustParse(path.Base(e.path))
	var plan uuid.UUID
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT o.runtime_plan_id FROM environments e JOIN execution_operations o ON o.id=e.runtime_operation_id WHERE e.id=$1`, env).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	// Each copy of the applied plan gets IDs and hashes of its own, as each real deployment does.
	seed := func(count int) {
		t.Helper()
		if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,runtime_outcome,runtime_plan_id,created_at)
SELECT gen_random_uuid(),p.workspace_id,$2,p.environment_id,(SELECT max(generation) FROM execution_operations WHERE environment_id=p.environment_id)+i,gen_random_uuid(),
	regexp_replace(regexp_replace(p.plan::text,'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}',gen_random_uuid()::text,'g'),'[0-9a-f]{64}',md5(random()::text)||md5(random()::text),'g')::jsonb,
	'visual','succeeded',1,'applied',p.id,now()-interval '1 day'+i*interval '1 millisecond'
FROM visual_plans p, generate_series(1,$3::int) i WHERE p.id=$1`, plan, a.id, count); err != nil {
			t.Fatal(err)
		}
		if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE environments SET execution_generation=(SELECT max(generation) FROM execution_operations WHERE environment_id=$1) WHERE id=$1`, env); err != nil {
			t.Fatal(err)
		}
	}
	// gzipSealed reports whether the workspace's base, gzipped as before, would still seal.
	gzipSealed := func() bool {
		t.Helper()
		tx, err := g.f.st.Pool.Begin(g.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(g.ctx)
		raw, err := portability.CaptureRecovery(g.ctx, tx, g.ws)
		if err != nil {
			t.Fatal("the base no longer fits the archive's own limits", err)
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		writer.Write(raw)
		writer.Close()
		envelope, err := g.f.st.Keys.Seal(compressed.Bytes(), "gzip-envelope-probe")
		return err == nil && len(envelope) <= deployment.RecoveryEnvelopeLimit
	}
	for gzipSealed() {
		seed(200)
	}
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
}

// Recovery checks a newer base against the exact runtime an older one applied, so a base still
// carries every deployment that applied a runtime. A workspace with enough of them still reaches
// the archive's record limit, and its cross-host deployments and export stop. This pins that limit
// until bases carry a compact record of applied history instead.
func TestDeploymentHistoryStillReachesTheArchiveLimit(t *testing.T) {
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
	applied := func(count int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,runtime_outcome,runtime_plan_id,created_at)
SELECT gen_random_uuid(),$1,$2,$3,COALESCE((SELECT max(generation) FROM execution_operations WHERE environment_id=$3),0)+i,gen_random_uuid(),'{}','visual','succeeded',1,'applied',$4,now()-interval '1 day'+i*interval '1 millisecond'
FROM generate_series(1,$5::int) i`, ws, in.ServerId, environment.Id, plan, count); err != nil {
			t.Fatal(err)
		}
	}
	capture := func() error {
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		_, err = portability.CaptureRecovery(ctx, tx, ws)
		return err
	}
	applied(9000)
	if err := capture(); err != nil {
		t.Fatal("9,000 applied deployments no longer fit a recovery base: ", err)
	}
	applied(1100)
	if err := capture(); !errors.Is(err, portability.ErrArchive) {
		t.Fatal("10,100 applied deployments fit a recovery base, so the archive limit this test pins has moved: ", err)
	}
}
