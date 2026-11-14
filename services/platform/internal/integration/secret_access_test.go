package integration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

type accessRecord struct {
	purpose, principal string
	account            *uuid.UUID
	items              string
	outcomes           []string
}

// accessRecords lists a workspace's secret access records, oldest first, with their outcomes.
func accessRecords(t *testing.T, f *fixture, ws *uuid.UUID) []accessRecord {
	t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), `SELECT a.purpose,a.principal_kind,a.account_id,a.items::text,
		COALESCE((SELECT array_agg(o.outcome ORDER BY o.at) FROM secret_access_outcomes o WHERE o.access_id=a.id),'{}')
		FROM secret_access a WHERE a.workspace_id IS NOT DISTINCT FROM $1 ORDER BY a.at,a.id`, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []accessRecord
	for rows.Next() {
		var r accessRecord
		if err = rows.Scan(&r.purpose, &r.principal, &r.account, &r.items, &r.outcomes); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func secretEnvironment(t *testing.T, f *fixture) (*browser, uuid.UUID, string) {
	t.Helper()
	owner := f.browser(t)
	session := owner.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	var p api.Project
	owner.call("POST", fmt.Sprintf("/api/v1/workspaces/%s/projects", ws), map[string]string{"name": "Website"}, 201, &p)
	var env api.Environment
	owner.call("POST", fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/environments", ws, p.Id), map[string]string{"name": "Production"}, 201, &env)
	return owner, ws, fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/environments/%s", ws, p.Id, env.Id)
}

// Storing a value is recorded, for its version and its author, and nothing is stored when the
// record cannot be written.
func TestSecretWritesAreRecordedFirst(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	var secret api.SecretVersion
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "recorded-canary"}, 201, &secret)
	records := accessRecords(t, f, &ws)
	if len(records) != 1 {
		t.Fatalf("records: %+v", records)
	}
	r := records[0]
	if r.purpose != "seal" || r.principal != "account" || r.account == nil || *r.account != f.owner || r.items != fmt.Sprintf(`[{"id": "%s", "kind": "secret-version"}]`, secret.Id) || len(r.outcomes) != 1 || r.outcomes[0] != "used" {
		t.Fatalf("record: %+v", r)
	}

	if _, err := f.st.Pool.Exec(ctx, `CREATE FUNCTION refuse_access() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'no records'; END $$;
		CREATE TRIGGER refuse_access BEFORE INSERT ON secret_access FOR EACH ROW EXECUTE FUNCTION refuse_access()`); err != nil {
		t.Fatal(err)
	}
	owner.call("POST", path+"/secrets", map[string]string{"name": "OTHER", "value": "unrecorded-canary"}, 500, nil)
	var versions int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secret_versions WHERE workspace_id=$1", ws).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("a value was stored without a record: %d versions", versions)
	}
}

// An export opens every credential under one record, made for the owner; a clone that copies
// secrets says so in its audit action.
func TestExportAndCopiesAreRecorded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	var secret api.SecretVersion
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "export-canary"}, 201, &secret)
	var encrypted api.WorkspaceArchive
	owner.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": "a long enough export passphrase"}, 200, &encrypted)
	records := accessRecords(t, f, &ws)
	last := records[len(records)-1]
	if last.purpose != "export" || last.principal != "account" || *last.account != f.owner || len(last.outcomes) != 1 || last.outcomes[0] != "used" {
		t.Fatalf("export record: %+v", last)
	}
	owner.call("POST", path+"/clone", api.CloneEnvironment{Name: "Plain"}, 201, nil)
	owner.call("POST", path+"/clone", api.CloneEnvironment{Name: "With secrets", CopySecrets: true}, 201, nil)
	var plain, copied int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE action='environment.clone'),count(*) FILTER (WHERE action='environment.clone_with_secrets') FROM audit_events WHERE workspace_id=$1", ws).Scan(&plain, &copied); err != nil {
		t.Fatal(err)
	}
	if plain != 1 || copied != 1 {
		t.Fatalf("clone actions: %d plain, %d with secrets", plain, copied)
	}
	// Each clone asked for every secret version rather than one it named. Neither had a secret to
	// copy or a database password to make.
	var cloneRecords []string
	for _, r := range accessRecords(t, f, &ws) {
		if strings.Contains(r.items, `"kind": "secret-version"`) && !strings.Contains(r.items, `"id"`) && r.purpose != "export" {
			cloneRecords = append(cloneRecords, fmt.Sprint(r.purpose, r.outcomes))
		}
	}
	slices.Sort(cloneRecords)
	if fmt.Sprint(cloneRecords) != "[copy[refused] seal[refused]]" {
		t.Fatalf("clone records: %v", cloneRecords)
	}
}

// A rotation is one record, for the operator's command, across every workspace.
func TestRewrapIsOneRecord(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "rotated-canary"}, 201, nil)
	if err := f.st.Rewrap(ctx, "test-operator", ""); err != nil {
		t.Fatal(err)
	}
	var purpose, operator string
	var outcome string
	if err := f.st.Pool.QueryRow(ctx, `SELECT a.purpose,a.operator_ref,o.outcome FROM secret_access a JOIN secret_access_outcomes o ON o.access_id=a.id WHERE a.workspace_id IS NULL AND a.principal_kind='operator'`).Scan(&purpose, &operator, &outcome); err != nil {
		t.Fatal(err)
	}
	// The tenant-facing record names the run; only the installation's key history names the operator.
	if started := keyEvents(t, f, "rewrap-started"); purpose != "rotate" || len(started) != 1 || operator != started[0].RunID.String() || outcome != "used" {
		t.Fatal(purpose, operator, outcome)
	}
	if n := len(accessRecords(t, f, &ws)); n != 1 {
		t.Fatalf("rewrap recorded per workspace: %d", n)
	}
}

// A record whose session never said how it ended is marked abandoned once, after the longest a
// session can last.
func TestAbandonedAccessIsSwept(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "swept-canary"}, 201, nil)
	stale, fresh := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{stale, fresh} {
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_access(id,workspace_id,purpose,principal_kind,job,items,item_count,generations,backend_id,reason) VALUES($1,$2,'operate','system','test','[]',0,'[0]','file:primary','operate')`, id, ws); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE secret_access SET at=now()-interval '1 hour' WHERE id=$1", stale); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.st.Broker.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[uuid.UUID]int{}
	rows, err := f.st.Pool.Query(ctx, "SELECT access_id FROM secret_access_outcomes WHERE outcome='abandoned'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		counts[id]++
	}
	rows.Close()
	if len(counts) != 1 || counts[stale] != 1 {
		t.Fatalf("abandoned: %v", counts)
	}
}

// A database clock dates every record, whatever the writer says.
func TestAccessTimesComeFromTheDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_access(id,at,purpose,principal_kind,job,items,item_count,generations,backend_id,reason) VALUES($1,'2000-01-01','token','system','test','[]',0,'[0]','file:primary','start-up')`, id); err != nil {
		t.Fatal(err)
	}
	var recent bool
	if err := f.st.Pool.QueryRow(ctx, "SELECT at > now()-interval '1 minute' FROM secret_access WHERE id=$1", id).Scan(&recent); err != nil || !recent {
		t.Fatal("a writer chose the record's time", err)
	}
}

// A caller the write would refuse spends no record and no budget: a viewer's refused secret
// write leaves nothing behind.
func TestRefusedCallersLeaveNoRecord(t *testing.T) {
	f := setup(t)
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", fmt.Sprintf("/api/v1/workspaces/%s/invitations", ws), map[string]string{"email": "access-viewer@example.test", "role": "viewer"}, 201, nil)
	invite := tokenFromLinkMail(t, f, "/invitation")
	viewer := f.browser(t)
	var session api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &session)
	viewer.csrf = session.CsrfToken
	viewer.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "refused-canary"}, 403, nil)
	if n := len(accessRecords(t, f, &ws)); n != 0 {
		t.Fatalf("a refused write was recorded: %d", n)
	}
}

// Enrolling and checking a second factor are recorded for the account, outside any workspace.
func TestSecondFactorIsRecorded(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	enableMFA(t, b)
	var purposes []string
	rows, err := f.st.Pool.Query(context.Background(), "SELECT purpose FROM secret_access WHERE workspace_id IS NULL AND account_id=$1 ORDER BY at,id", f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var purpose string
		if err = rows.Scan(&purpose); err != nil {
			t.Fatal(err)
		}
		purposes = append(purposes, purpose)
	}
	rows.Close()
	if fmt.Sprint(purposes) != "[seal authenticate]" {
		t.Fatalf("second factor records: %v", purposes)
	}
}
