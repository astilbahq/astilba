package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// environmentKey is an environment's key: the one saving its first secret made, or else one made
// as that does, a random key wrapped under its workspace's key at generation 1.
func environmentKey(t *testing.T, f *fixture, ws, env uuid.UUID) []byte {
	t.Helper()
	var existing []byte
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT wrapped FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2 AND state='active'`, ws, env).Scan(&existing); err == nil {
		key, err := cryptobox.OpenEnvironmentKey(workspaceKey(t, f, ws, 1), existing, ws, env, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	wrapped, err := cryptobox.WrapEnvironmentKey(workspaceKey(t, f, ws, 1), ws, env, 1, 1, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(context.Background(), `INSERT INTO environment_keys(workspace_id,environment_id,generation,state,workspace_generation,wrapped,kcv) VALUES($1,$2,1,'active',1,$3,$4)`, ws, env, wrapped, custody.KeyCheck(key)); err != nil {
		t.Fatal(err)
	}
	return key
}

// underEnvironment stores a secret version's value again under its environment's key, as a
// release that seals under environment keys stores it.
func underEnvironment(t *testing.T, f *fixture, ws, env, version uuid.UUID, key []byte, value string) []byte {
	t.Helper()
	sealed, err := cryptobox.SealUnderEnvironment(key, env, 1, []byte(value), secretIdentity(ws, version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(context.Background(), `UPDATE secret_versions SET encrypted_value=$2 WHERE workspace_id=$1 AND id=$3`, ws, sealed, version); err != nil {
		t.Fatal(err)
	}
	return sealed
}

// An environment's values sealed under its own key open through every read path, so a release
// that writes them can be rolled back to this one by redeploying. They count as converted.
func TestEnvironmentValuesOpenUnderTheirKey(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Sealed apart"}, 201, &env)
	path := x.prefix + "/" + env.Id.String()
	var secret api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Token", "value": "placeholder"}, 201, &secret)
	key := environmentKey(t, f, x.ws, env.Id)
	sealed := underEnvironment(t, f, x.ws, env.Id, secret.Id, key, "environment-canary")
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: secret.Id}
	if value, err := inspectValue(t, f.st, f.owner, x.ws, item, sealed); err != nil || string(value) != "environment-canary" {
		t.Fatal(string(value), err)
	}
	// The same bytes, edited to name another environment of the workspace, do not open with its
	// key.
	var other api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Another"}, 201, &other)
	environmentKey(t, f, x.ws, other.Id)
	moved := []byte(strings.Replace(string(sealed), env.Id.String(), other.Id.String(), 1))
	if string(moved) == string(sealed) {
		t.Fatal("the holder was not changed")
	}
	if _, err := inspectValue(t, f.st, f.owner, x.ws, item, moved); !errors.Is(err, cryptobox.ErrKey) {
		t.Fatal("a value under one environment's key opened as another's", err)
	}
	// A workspace export opens it.
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := (&portability.Service{Store: f.st}).Export(ctx, browserPrincipal(t, f.st, account, false), x.ws, "environment-keys-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if len(archive) == 0 {
		t.Fatal("empty export")
	}
	// Converting does not count it as under the keyring.
	counts, err := db.New(f.st.Pool).LegacyValueCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range counts {
		if c.Remaining != 0 {
			t.Fatalf("%d %s values counted as under the keyring", c.Remaining, c.Kind)
		}
	}
}

// Purging an environment destroys its key in the purge's transaction and records it, so every copy
// of its values stops opening, including those a recovery checkpoint captured before the purge.
// Recovery from such a checkpoint passes its key check and restores them erased; other
// environments' values still open, and their keys are kept.
func TestPurgeDestroysTheEnvironmentKey(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env, kept api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Shredded"}, 201, &env)
	x.owner.call("POST", x.prefix, map[string]string{"name": "Kept"}, 201, &kept)
	path, keptPath := x.prefix+"/"+env.Id.String(), x.prefix+"/"+kept.Id.String()
	var secret, other api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Token", "value": "placeholder"}, 201, &secret)
	x.owner.call("POST", keptPath+"/secrets", map[string]string{"name": "Token", "value": "placeholder"}, 201, &other)
	sealed := underEnvironment(t, f, x.ws, env.Id, secret.Id, environmentKey(t, f, x.ws, env.Id), "shredded-canary")
	keptSealed := underEnvironment(t, f, x.ws, kept.Id, other.Id, environmentKey(t, f, x.ws, kept.Id), "kept-canary")
	base := capturedBase(t, f, x.ws)

	// Nothing else destroys a key: not while the environment is active, nor by deleting it.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE environment_keys SET state='destroyed', wrapped=NULL, kcv=NULL WHERE environment_id=$1`, env.Id); err == nil {
		t.Fatal("an active environment's key was destroyed")
	}
	if _, err := f.st.Pool.Exec(ctx, `DELETE FROM environment_keys WHERE environment_id=$1`, env.Id); err == nil {
		t.Fatal("an environment key was deleted")
	}

	x.lifecycle(x.owner, path, "retire", "Shredded", 201)
	if v := x.purge(x.owner, path, "Shredded", []uuid.UUID{}, 201); v.State != api.EnvironmentStatePurged {
		t.Fatalf("purge %+v", v)
	}
	var state string
	var wrapped []byte
	if err := f.st.Pool.QueryRow(ctx, `SELECT state, wrapped FROM environment_keys WHERE environment_id=$1`, env.Id).Scan(&state, &wrapped); err != nil || state != "destroyed" || wrapped != nil {
		t.Fatal("the purged environment's key was kept", state, err)
	}
	destroyed := keyEvents(t, f, "environment-key-destroyed")
	if len(destroyed) != 1 || details(t, destroyed[0])["environment"] != env.Id.String() || destroyed[0].Reason.String != "environment purged" {
		t.Fatalf("destroyed events %+v", destroyed)
	}
	// The copy the checkpoint holds no longer opens; the kept environment's still does.
	if _, err := inspectValue(t, f.st, f.owner, x.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: secret.Id}, sealed); !errors.Is(err, secretbroker.ErrKeyDestroyed) {
		t.Fatal("a purged environment's value opened", err)
	}
	if value, err := inspectValue(t, f.st, f.owner, x.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: other.Id}, keptSealed); err != nil || string(value) != "kept-canary" {
		t.Fatal(string(value), err)
	}
	// The key is never made again for the purged environment.
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO environment_keys(workspace_id,environment_id,generation,state,workspace_generation,wrapped,kcv) VALUES($1,$2,2,'active',1,'\x01',$3)`, x.ws, env.Id, make([]byte, 32)); err == nil {
		t.Fatal("a purged environment got a new key")
	}

	// Recovery from the pre-purge checkpoint: its key check passes, the purged value stays erased,
	// and the kept environment's value stays under its key.
	if unopened, err := portability.CheckRecoveryKeys(recoverKeys(t, f.st, x.ws), base, nil); err != nil || len(unopened) != 0 {
		t.Fatal(err)
	}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, x.ws), base, f.owner); err != nil {
		t.Fatal(err)
	}
	var restored, restoredKept []byte
	var erased bool
	if err = tx.QueryRow(ctx, `SELECT encrypted_value, erased_at IS NOT NULL FROM secret_versions WHERE workspace_id=$1 AND id=$2`, x.ws, secret.Id).Scan(&restored, &erased); err != nil || restored != nil || !erased {
		t.Fatal("the purged value was restored", err)
	}
	if err = tx.QueryRow(ctx, `SELECT encrypted_value FROM secret_versions WHERE workspace_id=$1 AND id=$2`, x.ws, other.Id).Scan(&restoredKept); err != nil || string(restoredKept) != string(keptSealed) {
		t.Fatal("the kept environment's value moved", err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1 AND state='active'", kept.Id); n != 1 {
		t.Fatal("purging one environment touched another's key")
	}
}

// Recovery can bring a purge or end a preview that management's database has not seen, such as one
// restored from a backup taken before. Destroying ended environments' keys afterwards reaches both,
// and a preview's secret copies that a checkpoint still holds come back erased: the key that sealed
// them is gone.
func TestRecoveryDestroysTheKeysOfEndedEnvironments(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var purged, preview, live api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Purged elsewhere"}, 201, &purged)
	x.owner.call("POST", x.prefix, map[string]string{"name": "Preview"}, 201, &preview)
	x.owner.call("POST", x.prefix, map[string]string{"name": "Live"}, 201, &live)
	var copied api.SecretVersion
	x.owner.call("POST", x.prefix+"/"+preview.Id.String()+"/secrets", map[string]string{"name": "Token", "value": "placeholder"}, 201, &copied)
	x.lifecycle(x.owner, x.prefix+"/"+purged.Id.String(), "retire", "Purged elsewhere", 201)
	// A preview environment, as one is made: its purpose is fixed once set, so it is set with the
	// guards off, as other preview tests do.
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`SET session_replication_role=replica`, `UPDATE environments SET purpose='preview' WHERE id='` + preview.Id.String() + `'`, `SET session_replication_role=DEFAULT`} {
		if _, err = conn.Exec(ctx, statement); err != nil {
			conn.Release()
			t.Fatal(statement, err)
		}
	}
	conn.Release()
	keys := map[uuid.UUID][]byte{}
	for _, env := range []uuid.UUID{purged.Id, preview.Id, live.Id} {
		keys[env] = environmentKey(t, f, x.ws, env)
	}
	underEnvironment(t, f, x.ws, preview.Id, copied.Id, keys[preview.Id], "preview-copy-canary")
	// What a merge can leave: an environment purged by the checkpoint while its key lives on in
	// the restored database, and a preview with no open request whose secret copies were removed.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE environments SET state='purged', purged_at=now(), purged_by=$2, purged_without_host=true WHERE id=$1`, purged.Id, f.owner); err != nil {
		t.Fatal(err)
	}
	base := capturedBase(t, f, x.ws)
	if _, err = f.st.Pool.Exec(ctx, `DELETE FROM secret_versions WHERE workspace_id=$1 AND id=$2`, x.ws, copied.Id); err != nil {
		t.Fatal(err)
	}
	// Nothing but those ends destroys a key: the live environment's is refused.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE environment_keys SET state='destroyed', wrapped=NULL, kcv=NULL WHERE environment_id=$1`, live.Id); err == nil {
		t.Fatal("a live environment's key was destroyed")
	}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.Broker.DestroyEndedEnvironmentKeys(ctx, db.New(tx), x.ws); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	states := map[uuid.UUID]string{}
	rows, err := f.st.Pool.Query(ctx, `SELECT environment_id, state FROM environment_keys WHERE workspace_id=$1`, x.ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var env uuid.UUID
		var state string
		if err = rows.Scan(&env, &state); err != nil {
			t.Fatal(err)
		}
		states[env] = state
	}
	rows.Close()
	if states[purged.Id] != "destroyed" || states[preview.Id] != "destroyed" || states[live.Id] != "active" {
		t.Fatalf("key states %v", states)
	}
	reasons := map[string]bool{}
	for _, e := range keyEvents(t, f, "environment-key-destroyed") {
		reasons[e.Reason.String] = true
	}
	if !reasons["environment purge taken by recovery"] || !reasons["preview ended by recovery"] {
		t.Fatalf("reasons %v", reasons)
	}
	// The checkpoint's preview copy no longer opens: the merge passes its key check and restores
	// it erased, where the value it held would otherwise come back.
	if unopened, err := portability.CheckRecoveryKeys(recoverKeys(t, f.st, x.ws), base, nil); err != nil || len(unopened) != 0 {
		t.Fatal(err)
	}
	if tx, err = f.st.Pool.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, x.ws), base, f.owner); err != nil {
		t.Fatal(err)
	}
	var restored []byte
	var erased bool
	if err = tx.QueryRow(ctx, `SELECT encrypted_value, erased_at IS NOT NULL FROM secret_versions WHERE workspace_id=$1 AND id=$2`, x.ws, copied.Id).Scan(&restored, &erased); err != nil || restored != nil || !erased {
		t.Fatal("a copy under a destroyed key was restored", err)
	}
}

// A release that opens environment keys completes custody stage 5 once stage 4 is complete, and
// not before: an installation still to convert stays where it is.
func TestReadingEnvironmentKeysCompletesStageFive(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	keysJobDone(t, f)
	stage := func() int32 {
		t.Helper()
		s, err := db.New(f.st.Pool).CustodyStage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_stage SET stage=3`); err != nil {
		t.Fatal(err)
	}
	initKeys(t, f.st.Broker)
	if s := stage(); s != 3 {
		t.Fatalf("stage %d before converting", s)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_stage SET stage=4`); err != nil {
		t.Fatal(err)
	}
	before := len(keyEvents(t, f, "stage-completed"))
	initKeys(t, f.st.Broker)
	if s := stage(); s != 5 {
		t.Fatalf("stage %d after converting", s)
	}
	events := keyEvents(t, f, "stage-completed")
	if !slices.Equal([]string{details(t, events[len(events)-1])["stage"]}, []string{"5"}) || len(events) != before+1 {
		t.Fatal("stage 5 was not recorded once")
	}
}

// An environment's ID is unique within its workspace only, and importing keeps it, so an import
// can bring an environment with another workspace's environment ID, by accident or on purpose.
// Each workspace keeps its own key for it, and destroying one leaves the other as it was.
func TestEnvironmentKeysAreTheirWorkspaces(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Shared ID"}, 201, &env)
	environmentKey(t, f, x.ws, env.Id)
	// Another workspace with an environment of the same ID, as an import makes it.
	other := insertWorkspace(t, f, "Imported")
	initKeys(t, f.st.Broker)
	q := db.New(f.st.Pool)
	project, err := q.InsertProject(ctx, db.InsertProjectParams{WorkspaceID: other, ID: uuid.New(), Name: "Imported"})
	if err != nil {
		t.Fatal(err)
	}
	var spec []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT r.specification FROM specification_revisions r JOIN environments e ON e.workspace_id=r.workspace_id AND e.draft_revision_id=r.id WHERE e.workspace_id=$1 AND e.id=$2`, x.ws, env.Id).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	made, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer made.Rollback(ctx)
	revision, hash := uuid.New(), sha256.Sum256(spec)
	if _, err = db.New(made).InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: other, ProjectID: project.ID, ID: env.Id, Name: "Shared ID", DraftRevisionID: revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.New(made).InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: other, EnvironmentID: env.Id, ID: revision, AuthorID: f.owner, Specification: spec, ContentHash: hash[:]}); err != nil {
		t.Fatal(err)
	}
	if err = made.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The other workspace's environment gets a key of its own.
	environmentKey(t, f, other, env.Id)
	if n := x.count("SELECT count(DISTINCT workspace_id) FROM environment_keys WHERE environment_id=$1 AND state='active'", env.Id); n != 2 {
		t.Fatalf("%d workspaces hold a key for the shared environment ID", n)
	}
	// Purging the other workspace's environment destroys its key only.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE environments SET state='purged', retired_at=now(), retired_by=$3, purged_at=now(), purged_by=$3, purged_without_host=true WHERE workspace_id=$1 AND id=$2`, other, env.Id, f.owner); err != nil {
		t.Fatal(err)
	}
	if err = f.st.Broker.DestroyEnvironmentKeys(ctx, q, other, env.Id, "environment purged"); err != nil {
		t.Fatal(err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2 AND state='active'", x.ws, env.Id); n != 1 {
		t.Fatal("purging the other workspace's environment touched the original's key")
	}
}
