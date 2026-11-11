package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// oldPipelineRun makes the seeded run eligible for build log retention.
func oldPipelineRun(t *testing.T, f *fixture, parent uuid.UUID, result string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE pipeline_runs SET created_at=now()-interval '400 days',result=$2 WHERE id=$1`, parent, result); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionLeavesAResultItCannotPrune(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, _, parent := seedDiagnosticHistory(t, f)
	for _, tc := range []struct{ name, stored, want string }{
		{"scalar", `"logs"`, `"logs"`},
		{"array", `["logs"]`, `["logs"]`},
		{"object", `{"logs":"expired","kept":1}`, `{"kept":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldPipelineRun(t, f, parent, tc.stored)
			if err := f.st.PruneHistory(ctx); err != nil {
				t.Fatal("one run's stored result stopped retention", err)
			}
			var same bool
			if err := f.st.Pool.QueryRow(ctx, `SELECT result=$2::jsonb FROM pipeline_runs WHERE id=$1`, parent, tc.want).Scan(&same); err != nil || !same {
				t.Fatal("retention changed the wrong result", err)
			}
		})
	}
}

func TestRetentionCleanupsDoNotDependOnEachOther(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, _, parent := seedDiagnosticHistory(t, f)
	oldPipelineRun(t, f, parent, `{"logs":"expired"}`)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET created_at=now()-interval '400 days',note='expired' WHERE id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO password_resets(account_id,token_hash,expires_at) VALUES($1,'expired',now()-interval '1 day')`, f.owner); err != nil {
		t.Fatal(err)
	}
	// Any statement can fail for a reason nobody foresaw, on a row from any workspace.
	if _, err := f.st.Pool.Exec(ctx, `CREATE FUNCTION refuse_update() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `CREATE TRIGGER refuse_update BEFORE UPDATE ON pipeline_runs FOR EACH ROW EXECUTE FUNCTION refuse_update()`); err != nil {
		t.Fatal(err)
	}
	err := f.st.PruneHistory(ctx)
	if err == nil || !strings.Contains(err.Error(), "build_logs") {
		t.Fatalf("failed cleanup was not named: %v", err)
	}
	var note string
	if err = f.st.Pool.QueryRow(ctx, `SELECT note FROM execution_operations WHERE id=$1`, parent).Scan(&note); err != nil || note != "" {
		t.Fatal("a later failure undid an earlier cleanup", note, err)
	}
	var resets int
	if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM password_resets`).Scan(&resets); err != nil || resets != 0 {
		t.Fatal("a failure stopped a later cleanup", resets, err)
	}
}

// Rows are inserted without their foreign keys because only the selection is under test.
func TestPreviewSweepTakesEveryWorkspacesOldestFirst(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	defer conn.Exec(ctx, `SET session_replication_role=DEFAULT`)
	if _, err = conn.Exec(ctx, `SET session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	backlog, other := uuid.New(), uuid.New()
	for _, statement := range []string{
		`INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,enabled,configuration,next_discovery_at)
		 SELECT gen_random_uuid(),$1,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),1,1,true,'{}',now()-interval '1 year' FROM generate_series(1,50)`,
		`INSERT INTO preview_requests(id,workspace_id,policy_id,number,next_check_at)
		 SELECT gen_random_uuid(),$1,gen_random_uuid(),n,now()-interval '1 year' FROM generate_series(1,200) n`,
		`INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,enabled,configuration,next_discovery_at)
		 SELECT $2,$3,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),1,1,true,'{}',now()-interval '1 minute'`,
		`INSERT INTO preview_requests(id,workspace_id,policy_id,number,next_check_at) VALUES($2,$3,gen_random_uuid(),1,now()-interval '1 minute')`,
	} {
		args := []any{backlog}
		if strings.Contains(statement, "$3") {
			args = []any{uuid.New(), other}
			statement = strings.ReplaceAll(strings.ReplaceAll(statement, "$2", "$1"), "$3", "$2")
		}
		if _, err = conn.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	policies, err := f.st.Queries.DuePreviewPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 10 || !slices.ContainsFunc(policies, func(p db.DuePreviewPoliciesRow) bool { return p.WorkspaceID == other }) {
		t.Fatalf("selected %d policies without the other workspace's", len(policies))
	}
	requests, err := f.st.Queries.DuePreviewRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 20 || !slices.ContainsFunc(requests, func(r db.DuePreviewRequestsRow) bool { return r.WorkspaceID == other }) {
		t.Fatalf("selected %d requests without the other workspace's", len(requests))
	}
}

func TestExpiryNoticesPassAWorkspaceWhoseMailFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	failing := b.login("owner@example.test", f.password).Workspaces[0].Id
	f.st.Hosted = true
	other := uuid.New()
	address := "second-owner@example.test"
	otherOwner := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO accounts(id,email,name,password_hash,email_verified_at) SELECT $1,$2,'Second',password_hash,now() FROM accounts WHERE id=$3`, otherOwner, address, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Other')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, other, otherOwner); err != nil {
		t.Fatal(err)
	}
	// The owner's address fails on send, and the workspace expires first so it is at the head.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE accounts SET email='not an address',email_verified_at=now() WHERE id=$1`, f.owner); err != nil {
		t.Fatal(err)
	}
	for ws, until := range map[uuid.UUID]time.Duration{failing: time.Hour, other: 2 * time.Hour} {
		if err := f.st.GrantManagement(ctx, ws, time.Now().Add(until), 1); err != nil {
			t.Fatal(err)
		}
	}
	service := &billing.Service{Store: f.st, Now: time.Now}
	if err := service.NotifyExpiring(ctx, f.auth.Mail); err != nil {
		t.Fatal(err)
	}
	notified := func(ws uuid.UUID) bool {
		t.Helper()
		var done bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT notified_until IS NOT NULL FROM hosted_subscriptions WHERE workspace_id=$1`, ws).Scan(&done); err != nil {
			t.Fatal(err)
		}
		return done
	}
	if !notified(other) {
		t.Fatal("one workspace's mail failure stopped another workspace's notice")
	}
	if notified(failing) {
		t.Fatal("a notice that failed was recorded as sent, so it would never be retried")
	}
}

// Tampered rows are refused as an invalid archive, before anything is stored. Unmodified
// exports importing cleanly is covered by the portability and preview transfer tests.
func TestImportRefusesRowsThatSharedWorkCannotTrust(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _, _ := seedDiagnosticHistory(t, f)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	principal := browserPrincipal(t, f.st, account, true)
	const passphrase = "import check fixture passphrase"
	service := &portability.Service{Store: f.st}
	encrypted, err := service.Export(ctx, principal, ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := portability.Open(ctx, passphrase, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		tamper func(*portability.Archive)
	}{
		{"scalar pipeline result", func(a *portability.Archive) { a.Tables["pipeline_runs"][0]["result"] = jsontext.Value(`"logs"`) }},
		{"environment generation near overflow", func(a *portability.Archive) {
			a.Tables["environments"][0]["execution_generation"] = jsontext.Value(`9223372036854775807`)
		}},
		{"receipts too large to rebuild", func(a *portability.Archive) {
			a.Tables["cross_host_operations"][0]["receipts"] = jsontext.Value(`{"a":"` + strings.Repeat("x", 3<<20) + `"}`)
		}},
		{"number that renders huge", func(a *portability.Archive) {
			a.Tables["visual_plans"][0]["plan"] = jsontext.Value(`{"a":1e100000}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive portability.Archive
			if err := json.Unmarshal(plain, &archive); err != nil {
				t.Fatal(err)
			}
			tc.tamper(&archive)
			raw, err := stablejson.Marshal(archive)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := portability.Seal(ctx, passphrase, raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Import(ctx, principal, passphrase, sealed, ""); !errors.Is(err, portability.ErrArchive) {
				t.Fatalf("got %v, want an invalid archive", err)
			}
		})
	}
}

// Retention rebuilds rows with logs inside the database every workspace shares, at a cost
// far above their size, so an archive must not be able to hand it any.
func TestImportDropsLogsAnArchiveCarries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _, parent := seedDiagnosticHistory(t, f)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "import log fixture passphrase"
	encrypted, err := (&portability.Service{Store: f.st}).Export(ctx, browserPrincipal(t, f.st, account, true), ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := portability.Open(ctx, passphrase, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var archive portability.Archive
	if err = json.Unmarshal(plain, &archive); err != nil {
		t.Fatal(err)
	}
	archive.Tables["cross_host_operations"][0]["receipts"] = jsontext.Value(`{"complete/fixture":{"planHash":"kept","observation":{"resources":[{"state":"healthy","logs":"carried"}]}}}`)
	archive.Tables["pipeline_runs"][0]["result"] = jsontext.Value(`{"kept":1,"logs":"carried"}`)
	raw, err := stablejson.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := portability.Seal(ctx, passphrase, raw)
	if err != nil {
		t.Fatal(err)
	}
	destination := setup(t)
	importer, err := destination.st.Queries.AccountByID(ctx, destination.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&portability.Service{Store: destination.st}).Import(ctx, browserPrincipal(t, destination.st, importer, true), passphrase, sealed, ""); err != nil {
		t.Fatal(err)
	}
	var receipts, result string
	if err = destination.st.Pool.QueryRow(ctx, `SELECT h.receipts::text,p.result::text FROM cross_host_operations h JOIN pipeline_runs p ON p.id=h.id WHERE h.id=$1`, parent).Scan(&receipts, &result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(receipts, "logs") || strings.Contains(result, "logs") || !strings.Contains(receipts, "kept") || !strings.Contains(result, "kept") {
		t.Fatalf("imported receipts %s and result %s", receipts, result)
	}
}

func TestRewrapNamesTheRowItCannotRead(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	secret, version := uuid.New(), uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secrets(workspace_id,environment_id,id,name) VALUES($1,$2,$3,'Oversize')`, ws, env, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_versions(workspace_id,secret_id,id,encrypted_value,author_id) VALUES($1,$2,$3,decode(repeat('41',1048577),'hex'),$4)`, ws, secret, version, f.owner); err != nil {
		t.Fatal(err)
	}
	err := f.st.Rewrap(ctx)
	if err == nil || !strings.Contains(err.Error(), "secret_versions") || !strings.Contains(err.Error(), version.String()) || !strings.Contains(err.Error(), ws.String()) {
		t.Fatalf("rewrap failure did not name the row: %v", err)
	}
}
