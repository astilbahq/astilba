package integration

import (
	"context"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// Owners see every time management was given the keys to their workspace's values, a page at a
// time, with names for what still exists and how each ended. Nobody else sees them.
func TestSecretAccessRecordsAreForOwners(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Records"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	var secret api.SecretVersion
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": "records-canary"}, 201, &secret)

	var page api.SecretAccessPage
	b.call("GET", prefix+"/secret-access", nil, 200, &page)
	var sealed *api.SecretAccessRecord
	for i, r := range page.Records {
		if r.Purpose == api.SecretAccessRecordPurposeSeal && len(r.Items) == 1 && r.Items[0].Id != nil && *r.Items[0].Id == secret.Id {
			sealed = &page.Records[i]
		}
	}
	if sealed == nil || sealed.Principal.Kind != api.SecretAccessPrincipalKindAccount || sealed.Principal.AccountEmail == nil || *sealed.Principal.AccountEmail != "owner@example.test" || sealed.Items[0].Name == nil || *sealed.Items[0].Name != "DB_PASSWORD" || sealed.Outcome != api.SecretAccessRecordOutcomeUsed {
		t.Fatalf("the secret's seal record: %+v", sealed)
	}

	// A record whose session has not said how it ended, and enough others to need a second page.
	insert := func(n int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_access(id,workspace_id,purpose,principal_kind,job,items,item_count,generations,backend_id,reason)
SELECT gen_random_uuid(),$1,'inspect','system','records-test','[{"kind":"secret-version"}]',0,'[]','file:primary','test' FROM generate_series(1,$2::int)`, ws, n); err != nil {
			t.Fatal(err)
		}
	}
	insert(120)
	var first, second api.SecretAccessPage
	b.call("GET", prefix+"/secret-access", nil, 200, &first)
	// A record for every value of a kind names the kind and no item.
	if len(first.Records) != 100 || first.Next == nil || first.Records[0].Outcome != api.SecretAccessRecordOutcomeUnknown || first.Records[0].Principal.Job == nil || len(first.Records[0].Items) != 1 || first.Records[0].Items[0].Id != nil || first.Records[0].Items[0].Kind != "secret-version" {
		t.Fatalf("first page: %d records, next %v", len(first.Records), first.Next)
	}
	b.call("GET", prefix+"/secret-access?before="+first.Next.String(), nil, 200, &second)
	seen := map[uuid.UUID]bool{}
	for _, r := range append(first.Records, second.Records...) {
		if seen[r.Id] {
			t.Fatal("a record appeared on two pages", r.Id)
		}
		seen[r.Id] = true
	}
	if second.Next != nil || len(seen) != 120+len(page.Records) {
		t.Fatalf("pages held %d records, want %d", len(seen), 120+len(page.Records))
	}

	// Another workspace's owner sees none of them, and a viewer of this one is refused.
	hash, _ := f.auth.Passwords.Hash(f.password)
	person := func(email, role string, workspace uuid.UUID) *browser {
		t.Helper()
		id := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO accounts(id,email,name,password_hash) VALUES($1,$2,'Person',$3)`, id, email, hash); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,$3)`, workspace, id, role); err != nil {
			t.Fatal(err)
		}
		p := f.browser(t)
		p.login(email, f.password)
		return p
	}
	elsewhere := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Elsewhere')`, elsewhere); err != nil {
		t.Fatal(err)
	}
	person("records-other@example.test", "owner", elsewhere).call("GET", prefix+"/secret-access", nil, 404, nil)
	person("records-viewer@example.test", "viewer", ws).call("GET", prefix+"/secret-access", nil, 403, nil)

	// An account that has left is named by its ID alone.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_access SET account_id=(SELECT id FROM accounts WHERE email='records-other@example.test') WHERE id=$1`, sealed.Id); err != nil {
		t.Fatal(err)
	}
	b.call("GET", prefix+"/secret-access?before="+first.Next.String(), nil, 200, &second)
	for _, r := range second.Records {
		if r.Id == sealed.Id && (r.Principal.AccountId == nil || r.Principal.AccountEmail != nil) {
			t.Fatalf("a non-member's address was shown: %+v", r.Principal)
		}
	}
}

// Records go once they pass the retention period that `astilba migrate` set, with their
// outcomes, and not before.
func TestSecretAccessRecordsExpire(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	q := db.New(f.st.Pool)
	age := func(days int) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_access(id,workspace_id,purpose,principal_kind,job,items,item_count,generations,backend_id,reason) VALUES($1,$2,'inspect','system','records-test','[]',0,'[]','file:primary','test')`, id, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO secret_access_outcomes(access_id,outcome) VALUES($1,'used')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_access SET at=now()-make_interval(days => $2) WHERE id=$1`, id, days); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exists := func(id uuid.UUID) bool {
		t.Helper()
		var n int
		if err := f.st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM secret_access WHERE id=$1) + (SELECT count(*) FROM secret_access_outcomes WHERE access_id=$1)`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	// The hourly job may run too, so only what is left counts.
	old, kept := age(401), age(399)
	if _, err := q.ExpireSecretAccess(ctx, 1000); err != nil || exists(old) || !exists(kept) {
		t.Fatalf("after expiry: old %v, kept %v, %v", exists(old), exists(kept), err)
	}
	// A caller's temporary table cannot stand in for the setting.
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `CREATE TEMP TABLE secret_access_settings AS SELECT 1 AS retention_days`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `SELECT expire_secret_access(1000)`); err != nil || !exists(kept) {
		t.Fatalf("a temporary setting shortened retention: %v", err)
	}
	if _, err = conn.Exec(ctx, `DROP TABLE pg_temp.secret_access_settings`); err != nil {
		t.Fatal(err)
	}
	if err = q.SetSecretAccessRetention(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, err = q.ExpireSecretAccess(ctx, 1000); err != nil || exists(kept) {
		t.Fatalf("after shortening: kept %v, %v", exists(kept), err)
	}
}
