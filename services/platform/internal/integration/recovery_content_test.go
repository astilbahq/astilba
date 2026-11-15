package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/recoveryrows"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// capturedBase is a workspace's recovery data as a checkpoint captures it.
func capturedBase(t *testing.T, f *fixture, ws uuid.UUID) portability.Archive {
	t.Helper()
	ctx := context.Background()
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	raw, err := portability.CaptureRecovery(ctx, tx, ws)
	if err != nil {
		t.Fatal(err)
	}
	base, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func mergeBase(t *testing.T, f *fixture, ws uuid.UUID, base portability.Archive) error {
	t.Helper()
	ctx := context.Background()
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	return portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, ws), base, f.owner)
}

// storedRow is a row as recovery reads it.
func storedRow(t *testing.T, f *fixture, table string, id uuid.UUID) []byte {
	t.Helper()
	var raw []byte
	// inline-sql: the table and columns come from recoveryrows' compiled list
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT to_jsonb(q) FROM (SELECT "+recoveryrows.Columns(table)+" FROM "+table+" WHERE id=$1) q", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// restoredAs makes the checkpoint's copy of a secret version differ from the stored row as a
// restored one does, by its author, and records the stored row as restored from it in the form a
// release before content hashes recorded.
func restoredAs(t *testing.T, f *fixture, ws uuid.UUID, base portability.Archive) uuid.UUID {
	t.Helper()
	row := base.Tables["secret_versions"][0]
	var id uuid.UUID
	if err := json.Unmarshal(row["id"], &id); err != nil {
		t.Fatal(err)
	}
	row["author_id"] = jsontext.Value(`"` + uuid.New().String() + `"`)
	original, err := stablejson.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.New(f.st.Pool).UpsertRecoveryRowAttribution(context.Background(), db.UpsertRecoveryRowAttributionParams{WorkspaceID: ws, TableName: "secret_versions", SourceHash: recoveryrows.Hash(original), AttributedHash: recoveryrows.Hash(storedRow(t, f, "secret_versions", id))}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A checkpoint taken before a rewrap still merges after it, both a row as the checkpoint holds it
// and one a recovery restored: the rewrap moves recovery's record of the restored row to its
// content form before it moves the row's key.
func TestCheckpointFromBeforeARewrapStillMerges(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "rewrapped"}, 201, nil)
	// A value under the keyring, which is what a rewrap moves.
	asLegacy(t, f, ws, "rewrapped")
	keysSettled(t, f.st)
	unchanged := capturedBase(t, f, ws)
	restored := capturedBase(t, f, ws)
	id := restoredAs(t, f, ws, restored)
	before := storedRow(t, f, "secret_versions", id)
	ring := keysOf(f.st)
	rotated, _ := cryptobox.New("rotated")
	ring.Keys[rotated.Active] = rotated.Keys[rotated.Active]
	ring.Active = rotated.Active
	if err := f.st.Rewrap(ctx, "operator@example.test", ""); err != nil {
		t.Fatal(err)
	}
	after := storedRow(t, f, "secret_versions", id)
	if recoveryrows.Hash(after) == recoveryrows.Hash(before) {
		t.Fatal("the rewrap left the value as it was")
	}
	var attributed string
	if err := f.st.Pool.QueryRow(ctx, `SELECT attributed_hash FROM cross_host_recovery_rows WHERE workspace_id=$1 AND table_name='secret_versions'`, ws).Scan(&attributed); err != nil || attributed != recoveryrows.ContentHash("secret_versions", after) {
		t.Fatal("the restored row's record was not moved to its content form", err)
	}
	if err := mergeBase(t, f, ws, unchanged); err != nil {
		t.Fatal("a checkpoint from before the rewrap:", err)
	}
	if err := mergeBase(t, f, ws, restored); err != nil {
		t.Fatal("a checkpoint a restored row came from:", err)
	}
}

// A restored row recorded as stored, by a release before content hashes, still counts while the
// row is as it was; a checkpoint row nothing was restored from still conflicts.
func TestRestoredRowRecordedAsStoredStillCounts(t *testing.T) {
	f := setup(t)
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "restored"}, 201, nil)
	base := capturedBase(t, f, ws)
	restoredAs(t, f, ws, base)
	if err := mergeBase(t, f, ws, base); err != nil {
		t.Fatal(err)
	}
	foreign := capturedBase(t, f, ws)
	foreign.Tables["secret_versions"][0]["author_id"] = jsontext.Value(`"` + uuid.New().String() + `"`)
	if err := mergeBase(t, f, ws, foreign); err == nil || !strings.Contains(err.Error(), "recovery conflicts with immutable secret_versions") {
		t.Fatalf("a checkpoint row no restore came from: %v", err)
	}
}

// Moving the records to their content form is safe to stop and run again: a workspace already
// moved is left alone, and one whose transaction was rolled back is moved next time.
func TestRekeyingRestoredRowsResumes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ring := keysOf(f.st)
	var workspaces []uuid.UUID
	for _, name := range []string{"First", "Second"} {
		ws := insertWorkspace(t, f, name)
		id := uuid.New()
		sealed, err := ring.Seal([]byte("build"), pipeline.EncryptionIdentity(ws, id, "build-secret"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO build_secret_versions(id,workspace_id,name,encrypted_value) VALUES($1,$2,'TOKEN',$3)`, id, ws, sealed); err != nil {
			t.Fatal(err)
		}
		if err = db.New(f.st.Pool).UpsertRecoveryRowAttribution(ctx, db.UpsertRecoveryRowAttributionParams{WorkspaceID: ws, TableName: "build_secret_versions", SourceHash: "checkpoint-" + name, AttributedHash: recoveryrows.Hash(storedRow(t, f, "build_secret_versions", id))}); err != nil {
			t.Fatal(err)
		}
		workspaces = append(workspaces, ws)
	}
	rekey := func(ws uuid.UUID, commit bool) int64 {
		t.Helper()
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var n int64
		if ws == uuid.Nil {
			n, err = recoveryrows.RekeyAll(ctx, tx)
		} else {
			n, err = recoveryrows.Rekey(ctx, tx, ws)
		}
		if err != nil {
			t.Fatal(err)
		}
		if commit {
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return n
	}
	if n := rekey(workspaces[0], true); n != 1 {
		t.Fatalf("first workspace: %d", n)
	}
	// Stopped part way: the second workspace's change is rolled back.
	if n := rekey(workspaces[1], false); n != 1 {
		t.Fatalf("second workspace: %d", n)
	}
	if n := rekey(uuid.Nil, true); n != 1 {
		t.Fatalf("the next run moved %d records, want only the second workspace's", n)
	}
	if n := rekey(uuid.Nil, true); n != 0 {
		t.Fatalf("a run after everything moved changed %d records", n)
	}
}
