package integration

import (
	"context"
	"encoding/json/jsontext"
	"maps"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// Recovery never brings an erased secret value back. A checkpoint's erasure is taken over a value
// the database still holds, and an erasure the database holds is kept over a checkpoint that still
// has the value. A checkpoint's new secret can take the name its retired one freed.
func TestRecoveryNeverRestoresAnErasedSecret(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Recovery"}, 201, &project)
	var env api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := base + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var erasedInCheckpoint, erasedHere api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Checkpoint", "value": "dummy-recovery-one"}, 201, &erasedInCheckpoint)
	b.call("POST", path+"/secrets", map[string]string{"name": "Database", "value": "dummy-recovery-two"}, 201, &erasedHere)

	// The checkpoint is taken while both values exist; then this database retires one secret.
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portability.CaptureRecovery(ctx, tx, ws)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.call("DELETE", path+"/secrets/"+erasedHere.SecretId.String(), nil, 204, nil)
	archive, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	// And the checkpoint holds the other secret's retirement, which this database never saw, and a
	// new secret under the name that retirement freed, ahead of it in the archive.
	const at = `"2026-11-13T00:00:00Z"`
	reused := uuid.New()
	for _, row := range archive.Tables["secrets"] {
		if string(row["id"]) == `"`+erasedInCheckpoint.SecretId.String()+`"` {
			row["retired_at"] = jsontext.Value(at)
			successor := maps.Clone(row)
			successor["id"], successor["retired_at"] = jsontext.Value(`"`+reused.String()+`"`), jsontext.Value("null")
			archive.Tables["secrets"] = append([]portability.Row{successor}, archive.Tables["secrets"]...)
			break
		}
	}
	for _, row := range archive.Tables["secret_versions"] {
		if string(row["id"]) == `"`+erasedInCheckpoint.Id.String()+`"` {
			row["encrypted_value"], row["erased_at"] = jsontext.Value("null"), jsontext.Value(at)
		}
	}

	owner := f.owner
	f.server.Close()
	if err = f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	merge := func() {
		t.Helper()
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = store.RequireOffline(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err = portability.MergeRecovery(ctx, tx, archive, owner); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Merging again over its own result changes nothing.
	for range 2 {
		merge()
		for _, secret := range []api.SecretVersion{erasedInCheckpoint, erasedHere} {
			var erased bool
			if err = f.st.Pool.QueryRow(ctx, "SELECT encrypted_value IS NULL AND erased_at IS NOT NULL FROM secret_versions WHERE workspace_id=$1 AND id=$2", ws, secret.Id).Scan(&erased); err != nil {
				t.Fatal(err)
			}
			if !erased {
				t.Fatalf("secret %s after recovery kept its value", secret.Name)
			}
		}
		var live int
		if err = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secrets WHERE workspace_id=$1 AND name='Checkpoint' AND retired_at IS NULL AND id=$2", ws, reused).Scan(&live); err != nil || live != 1 {
			t.Fatalf("the new secret under the freed name: %d %v", live, err)
		}
	}
	var kept int
	if err = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id <> ALL($2::uuid[])", ws, []uuid.UUID{erasedInCheckpoint.Id, erasedHere.Id}).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("unexpected secret versions: %d %v", kept, err)
	}
}
