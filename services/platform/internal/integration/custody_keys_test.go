package integration

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ownerWorkspace is the workspace the fixture bootstrapped.
func ownerWorkspace(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var ws uuid.UUID
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT workspace_id FROM memberships WHERE account_id=$1`, f.owner).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// installationID is the fixture's installation ID.
func installationID(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	id, err := db.New(f.st.Pool).InstallationID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// workspaceKey unwraps a workspace's key at a generation with the fixture's keyring, as a later
// release that seals v2 values holds it.
func workspaceKey(t *testing.T, f *fixture, ws uuid.UUID, generation int64) []byte {
	t.Helper()
	var wrapped []byte
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT wrapped FROM workspace_key_wrappings WHERE workspace_id=$1 AND generation=$2 AND purpose='inspect'`, ws, generation).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	backend, err := custody.NewFile(keysOf(f.st))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := backend.Unwrap(context.Background(), custody.ClassServe, wrapped, custody.Context{Installation: installationID(t, f), Scope: custody.ScopeWorkspace, Workspace: ws, Generation: generation, Purpose: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	return opened.Key
}

// insertWorkspace adds a workspace as an older release would, with no keys, owned by the fixture's
// owner.
func insertWorkspace(t *testing.T, f *fixture, name string) uuid.UUID {
	t.Helper()
	ws := uuid.New()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,$2)`, ws, name); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, f.owner); err != nil {
		t.Fatal(err)
	}
	return ws
}

// keysJobDone waits for the key job the fixture's workers run at start, after which nothing makes
// keys in the background for ten minutes.
func keysJobDone(t *testing.T, f *fixture) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var done bool
		if err := f.st.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_custody_keys_v1' AND state='completed')`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("the key job did not run")
}

func initKeys(t *testing.T, b *secretbroker.Broker) {
	t.Helper()
	if more, err := b.InitKeys(context.Background(), func() bool { return true }); err != nil || more {
		t.Fatal(more, err)
	}
}

// A new workspace gets its first generation with the workspace: wrapped once per purpose for the
// registered backend, recorded, and nothing else.
func TestNewWorkspacesHaveKeys(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	var state string
	var sealable bool
	if err := f.st.Pool.QueryRow(ctx, `SELECT state,sealable FROM workspace_keys WHERE workspace_id=$1 AND generation=1`, ws).Scan(&state, &sealable); err != nil || state != "active" || !sealable {
		t.Fatal(state, sealable, err)
	}
	rows, err := f.st.Pool.Query(ctx, `SELECT purpose FROM workspace_key_wrappings WHERE workspace_id=$1 AND generation=1 AND backend_id='file:primary' ORDER BY purpose`, ws)
	if err != nil {
		t.Fatal(err)
	}
	purposes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !slices.Equal(purposes, custody.Purposes(custody.ScopeWorkspace)) || len(purposes) != 8 {
		t.Fatal(purposes, err)
	}
	// The installation's own keys may be made meanwhile, in the background.
	created := slices.DeleteFunc(keyEvents(t, f, "key-created"), func(e db.InstallationKeyEvent) bool { return e.WorkspaceID == nil })
	if len(created) != 1 || *created[0].WorkspaceID != ws || created[0].Generation.Int64 != 1 || details(t, created[0])["source"] != "new-workspace" {
		t.Fatalf("events %+v", created)
	}
	// Each wrapping opens to the key its check value names, under its own purpose only.
	key := workspaceKey(t, f, ws, 1)
	var kcv []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT kcv FROM workspace_keys WHERE workspace_id=$1`, ws).Scan(&kcv); err != nil || !custody.CheckKey(key, kcv) {
		t.Fatal("key check value does not match", err)
	}
	var wrapped []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT wrapped FROM workspace_key_wrappings WHERE workspace_id=$1 AND purpose='seal'`, ws).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	backend, _ := custody.NewFile(keysOf(f.st))
	if _, err = backend.Unwrap(ctx, custody.ClassServe, wrapped, custody.Context{Installation: installationID(t, f), Scope: custody.ScopeWorkspace, Workspace: ws, Generation: 1, Purpose: "deliver"}); !errors.Is(err, custody.ErrInvalid) {
		t.Fatal("a seal wrapping opened for deliver", err)
	}
}

// Creating keys reaches every workspace without them, and the installation and token keys, once
// each however many run at once, and records the stage as complete once.
func TestInitKeysIsResumableAndRunsOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// The job the workers run at start has made the installation's keys and completed the stage;
	// workspaces an older release creates afterwards are left for the next run.
	keysJobDone(t, f)
	var older []uuid.UUID
	for i := range 5 {
		older = append(older, insertWorkspace(t, f, "Older "+string(rune('a'+i))))
	}
	// A run out of time leaves the rest for the next.
	calls := 0
	more, err := f.st.Broker.InitKeys(ctx, func() bool { calls++; return calls < 3 })
	if err != nil || !more {
		t.Fatal(more, err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			backend, err := custody.NewFile(keysOf(f.st))
			if err != nil {
				errs[i] = err
				return
			}
			b := secretbroker.New(db.New(f.st.Pool), backend, nil)
			if errs[i] = b.Install(ctx, f.st.Pool, uuid.Nil, false); errs[i] != nil {
				return
			}
			for {
				more, err := b.InitKeys(ctx, func() bool { return true })
				if err != nil || !more {
					errs[i] = err
					return
				}
			}
		})
	}
	wg.Wait()
	if err = errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	var keys, scopes int
	if err = f.st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM workspace_keys WHERE workspace_id = ANY($1)), (SELECT count(*) FROM installation_keys)`, older).Scan(&keys, &scopes); err != nil || keys != len(older) || scopes != 2 {
		t.Fatal(keys, scopes, err)
	}
	if created := keyEvents(t, f, "key-created"); len(created) != 1+len(older)+2 {
		t.Fatalf("%d key events", len(created))
	}
	if stage, err := db.New(f.st.Pool).CustodyStage(ctx); err != nil || stage != dbmigrate.CustodyStage {
		t.Fatal(stage, err)
	}
	if completed := keyEvents(t, f, "stage-completed"); len(completed) != 1 || details(t, completed[0])["stage"] != "2" {
		t.Fatalf("stage events %+v", completed)
	}
	// Nothing is left, so another run changes nothing.
	initKeys(t, f.st.Broker)
	if created := keyEvents(t, f, "key-created"); len(created) != 1+len(older)+2 {
		t.Fatalf("%d key events after another run", len(created))
	}
}

// A workspace session holds its workspace's live generations, so it opens values sealed under them
// as well as under the keyring: values a later release writes open on this one.
func TestSessionsOpenV2ValuesOfTheirOwnWorkspace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := ownerWorkspace(t, f)
	b := insertWorkspace(t, f, "Other")
	initKeys(t, f.st.Broker)
	item := uuid.New()
	sealed, err := cryptobox.SealUnder(workspaceKey(t, f, a, 1), cryptobox.ScopeWorkspace, 1, []byte("written by a later release"), secretIdentity(a, item))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := keysOf(f.st).Seal([]byte("written before keys"), secretIdentity(a, item))
	if err != nil {
		t.Fatal(err)
	}
	open := func(ws uuid.UUID, blob []byte) ([]byte, error) {
		t.Helper()
		s, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{{Kind: secretbroker.SecretVersion, ID: item}}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close(ctx, secretbroker.Used)
		return s.Open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: item}, blob)
	}
	for _, blob := range [][]byte{sealed, legacy} {
		if value, err := open(a, blob); err != nil || !strings.HasPrefix(string(value), "written") {
			t.Fatal(string(value), err)
		}
	}
	// Another workspace's session holds its own generation 1, which is not this value's key.
	if _, err = open(b, sealed); !errors.Is(err, cryptobox.ErrKey) {
		t.Fatal("another workspace opened the value", err)
	}
	// The workspace's key under another workspace's identity does not open it either.
	moved, err := cryptobox.SealUnder(workspaceKey(t, f, a, 1), cryptobox.ScopeWorkspace, 1, []byte("x"), secretIdentity(b, item))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = open(b, moved); !errors.Is(err, cryptobox.ErrKey) {
		t.Fatal("a value sealed under another workspace's key opened", err)
	}
	// A generation that is not live does not open, and does not read as a session that ended:
	// another session would hold the same generations.
	later, err := cryptobox.SealUnder(workspaceKey(t, f, a, 1), cryptobox.ScopeWorkspace, 2, []byte("x"), secretIdentity(a, item))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = open(a, later); !errors.Is(err, cryptobox.ErrKey) || secretbroker.SessionFailure(err) {
		t.Fatal(err)
	}
	// The record names the generations the session held.
	var generations string
	if err = f.st.Pool.QueryRow(ctx, `SELECT generations::text FROM secret_access WHERE workspace_id=$1 AND purpose='inspect' ORDER BY at DESC LIMIT 1`, a).Scan(&generations); err != nil || generations != "[0, 1]" {
		t.Fatal(generations, err)
	}
	// Fields kept for per-environment keys are refused until that release.
	reserved := strings.Replace(string(sealed), `"version":2`, `"holder":"`+uuid.NewString()+`","version":2`, 1)
	environment := strings.Replace(string(sealed), `"scope":"workspace"`, `"scope":"environment"`, 1)
	for _, blob := range []string{reserved, environment} {
		if blob == string(sealed) {
			t.Fatal("the reserved field was not set")
		}
		if _, err = open(a, []byte(blob)); !errors.Is(err, cryptobox.ErrKey) {
			t.Fatal("a reserved field was accepted", err)
		}
	}
}

// A wrapping that opens to some other key fails at Begin, before any value is opened, and the
// record says the session failed.
func TestSessionsCheckEachUnwrappedKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	other, _, err := custody.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := custody.NewFile(keysOf(f.st))
	wrapped, _, err := backend.Wrap(ctx, custody.ClassServe, other, custody.Context{Installation: installationID(t, f), Scope: custody.ScopeWorkspace, Workspace: ws, Generation: 1, Purpose: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `UPDATE workspace_key_wrappings SET wrapped=$2 WHERE workspace_id=$1 AND purpose='inspect'`, ws, wrapped); err != nil {
		t.Fatal(err)
	}
	_, err = f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	if !errors.Is(err, secretbroker.ErrKeyCheck) {
		t.Fatal(err)
	}
	var outcome string
	if err = f.st.Pool.QueryRow(ctx, `SELECT o.outcome FROM secret_access a JOIN secret_access_outcomes o ON o.access_id=a.id WHERE a.workspace_id=$1 AND a.purpose='inspect'`, ws).Scan(&outcome); err != nil || outcome != "failed" {
		t.Fatal(outcome, err)
	}
	// A live generation with no wrapping for the purpose is refused the same way.
	if _, err = f.st.Pool.Exec(ctx, `DELETE FROM custody_backends`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Kinds: []secretbroker.Kind{secretbroker.SecretVersion}}); !errors.Is(err, secretbroker.ErrMissingWrapping) {
		t.Fatal(err)
	}
}

// Key generations only move forward, a seal wrapping exists only for an active, sealable
// generation, and keys go only with their workspace.
func TestKeyGenerationsOnlyMoveForward(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	exec := func(sql string, args ...any) error {
		t.Helper()
		_, err := f.st.Pool.Exec(ctx, sql, args...)
		return err
	}
	kcv := bytes.Repeat([]byte{1}, 32)
	for _, refused := range []struct {
		name string
		sql  string
		args []any
	}{
		{"a generation that is not new", `INSERT INTO workspace_keys(workspace_id,generation,state,kcv) VALUES($1,1,'active',$2)`, []any{ws, kcv}},
		{"a generation that does not start active", `INSERT INTO workspace_keys(workspace_id,generation,state,kcv) VALUES($1,2,'retiring',$2)`, []any{ws, kcv}},
		{"a key check value changed", `UPDATE workspace_keys SET kcv=$2 WHERE workspace_id=$1`, []any{ws, kcv}},
		{"a generation renumbered", `UPDATE workspace_keys SET generation=7 WHERE workspace_id=$1`, []any{ws}},
		{"a key deleted", `DELETE FROM workspace_keys WHERE workspace_id=$1`, []any{ws}},
		{"a wrapping deleted while in use", `DELETE FROM workspace_key_wrappings WHERE workspace_id=$1 AND purpose='deliver'`, []any{ws}},
		{"a wrapping moved", `UPDATE workspace_key_wrappings SET purpose='copy' WHERE workspace_id=$1 AND purpose='deliver'`, []any{ws}},
	} {
		if err := exec(refused.sql, refused.args...); err == nil {
			t.Errorf("%s was allowed", refused.name)
		}
	}
	// Leaving active removes the seal wrapping, and the generation never seals again.
	if err := exec(`UPDATE workspace_keys SET state='retiring' WHERE workspace_id=$1`, ws); err != nil {
		t.Fatal(err)
	}
	var seal, all int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE purpose='seal'), count(*) FROM workspace_key_wrappings WHERE workspace_id=$1`, ws).Scan(&seal, &all); err != nil || seal != 0 || all != 7 {
		t.Fatal(seal, all, err)
	}
	for _, refused := range []string{
		`UPDATE workspace_keys SET state='active' WHERE workspace_id=$1`,
		`INSERT INTO workspace_key_wrappings(workspace_id,generation,purpose,backend_id,wrapped) VALUES($1,1,'seal','file:primary','\x01')`,
	} {
		if err := exec(refused, ws); err == nil {
			t.Errorf("%q was allowed", refused)
		}
	}
	if err := exec(`UPDATE workspace_keys SET sealable=false WHERE workspace_id=$1`, ws); err != nil {
		t.Fatal(err)
	}
	if err := exec(`UPDATE workspace_keys SET sealable=true WHERE workspace_id=$1`, ws); err == nil {
		t.Error("a generation became sealable again")
	}
	// A new generation must be above the old, and retiring the old removes every wrapping.
	if err := exec(`INSERT INTO workspace_keys(workspace_id,generation,state,kcv) VALUES($1,2,'active',$2)`, ws, kcv); err != nil {
		t.Fatal(err)
	}
	if err := exec(`UPDATE workspace_keys SET state='retired' WHERE workspace_id=$1 AND generation=1`, ws); err != nil {
		t.Fatal(err)
	}
	var retiredAt *string
	if err := f.st.Pool.QueryRow(ctx, `SELECT retired_at::text, (SELECT count(*) FROM workspace_key_wrappings WHERE workspace_id=$1 AND generation=1) FROM workspace_keys WHERE workspace_id=$1 AND generation=1`, ws).Scan(&retiredAt, &all); err != nil || retiredAt == nil || all != 0 {
		t.Fatal(retiredAt, all, err)
	}
	// Deleting a workspace deletes its keys.
	gone := insertWorkspace(t, f, "Deleted")
	initKeys(t, f.st.Broker)
	if err := exec(`DELETE FROM memberships WHERE workspace_id=$1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM workspaces WHERE id=$1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM workspace_keys WHERE workspace_id=$1`, gone).Scan(&all); err != nil || all != 0 {
		t.Fatal(all, err)
	}
	// The installation's keys are never deleted.
	if err := exec(`DELETE FROM installation_keys`); err == nil {
		t.Error("installation keys were deleted")
	}
}

// Keys wrapped for one set of backends are not stored once the set has changed, and an instance
// that lacks a backend new keys must be wrapped for makes none.
func TestNewKeysFollowTheBackendSet(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	keys, err := f.st.Broker.Create(ctx, secretbroker.Request{Workspace: ws, Principal: secretbroker.Account(f.owner)})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	if _, err = f.st.Pool.Exec(ctx, `UPDATE custody_backends_version SET version=version+1`); err != nil {
		t.Fatal(err)
	}
	err = f.st.Transaction(ctx, func(q *db.Queries) error { return store.CreateWorkspace(ctx, q, keys, "Raced", f.owner) })
	if !errors.Is(err, secretbroker.ErrBackendsChanged) {
		t.Fatal(err)
	}
	var exists bool
	if err = f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=$1)`, ws).Scan(&exists); err != nil || exists {
		t.Fatal("the workspace was created without its keys", err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO custody_backends(backend_id,role) VALUES('awskms@2','write')`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Broker.Create(ctx, secretbroker.Request{Workspace: uuid.New(), Principal: secretbroker.Account(f.owner)}); !errors.Is(err, secretbroker.ErrBackendNotConfigured) {
		t.Fatal(err)
	}
}

// Token key generations stay resident: a token under one an instance has not loaded is worth
// retrying, and opens once the instance loads it; each instance reports what it holds.
func TestTokenKeysLoadAsTheyAppear(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	initKeys(t, f.st.Broker)
	tokens, err := f.st.Broker.Tokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokenKey := func(generation int64) []byte {
		t.Helper()
		var wrapped []byte
		if err := f.st.Pool.QueryRow(ctx, `SELECT wrapped FROM installation_key_wrappings WHERE scope='token' AND generation=$1 AND purpose='token'`, generation).Scan(&wrapped); err != nil {
			t.Fatal(err)
		}
		backend, _ := custody.NewFile(keysOf(f.st))
		opened, err := backend.Unwrap(ctx, custody.ClassInstallation, wrapped, custody.Context{Installation: installationID(t, f), Scope: custody.ScopeToken, Generation: generation, Purpose: "token"})
		if err != nil {
			t.Fatal(err)
		}
		return opened.Key
	}
	first, err := cryptobox.SealUnder(tokenKey(1), cryptobox.ScopeToken, 1, []byte("a@example.test"), "reset-mail:v1")
	if err != nil {
		t.Fatal(err)
	}
	if email, err := tokens.Open(secretbroker.ResetMail, first); err != nil || string(email) != "a@example.test" {
		t.Fatal(string(email), err)
	}
	// Under another kind's identity it does not open.
	if _, err = tokens.Open(secretbroker.DeviceToken, first); !errors.Is(err, cryptobox.ErrKey) {
		t.Fatal(err)
	}
	// Generation 2 does not exist yet: retry.
	second, err := cryptobox.SealUnder(bytes.Repeat([]byte{2}, 32), cryptobox.ScopeToken, 2, []byte("b@example.test"), "reset-mail:v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tokens.Open(secretbroker.ResetMail, second); !errors.Is(err, secretbroker.ErrTokenKeyNotLoaded) {
		t.Fatal(err)
	}
	var generations []int64
	if err = f.st.Pool.QueryRow(ctx, `SELECT token_generations FROM custody_instances WHERE instance_id=$1`, tokens.Instance()).Scan(&generations); err != nil || !slices.Equal(generations, []int64{1}) {
		t.Fatal(generations, err)
	}
	if loaded := keyEvents(t, f, "token-key-loaded"); len(loaded) != 1 || loaded[0].Generation.Int64 != 1 {
		t.Fatalf("events %+v", loaded)
	}
	// Legacy tokens still open.
	legacy, err := tokens.Seal(secretbroker.ResetMail, []byte("c@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if header, err := cryptobox.ReadHeader(legacy); err != nil || header.Version != 1 {
		t.Fatal("tokens are issued under the keyring until values are sealed as v2", err)
	}
	if email, err := tokens.Open(secretbroker.ResetMail, legacy); err != nil || string(email) != "c@example.test" {
		t.Fatal(err)
	}
}

// A rewrap moves every key wrapping onto the active keyring key, and leaves v2 values as they are,
// since their data keys are under the workspace key.
func TestRewrapMovesKeyWrappings(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	initKeys(t, f.st.Broker)
	item := uuid.New()
	sealed, err := cryptobox.SealUnder(workspaceKey(t, f, ws, 1), cryptobox.ScopeWorkspace, 1, []byte("v2"), secretIdentity(ws, item))
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := f.st.Broker.BeginRotation(ctx, "operator@example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := rotation.Rewrap(ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: item}, sealed); err != nil || !bytes.Equal(again, sealed) {
		t.Fatal("a v2 value changed in a rewrap", err)
	}
	rotation.Close(ctx, secretbroker.Used)
	ring := keysOf(f.st)
	next, err := cryptobox.New("next")
	if err != nil {
		t.Fatal(err)
	}
	previous := ring.Active
	ring.Keys["next"], ring.Active = next.Keys["next"], "next"
	if err = f.st.Rewrap(ctx, "operator@example.test", "yearly"); err != nil {
		t.Fatal(err)
	}
	finished := keyEvents(t, f, "rewrap-finished")
	if wrappings := details(t, finished[len(finished)-1])["wrappings"]; wrappings != "13" {
		t.Fatalf("rewrapped %s wrappings", wrappings)
	}
	// Without the key that wrapped them, the workspace key still opens, and with it the v2 value.
	delete(ring.Keys, previous)
	s, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{{Kind: secretbroker.SecretVersion, ID: item}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, secretbroker.Used)
	if value, err := s.Open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: item}, sealed); err != nil || string(value) != "v2" {
		t.Fatal(string(value), err)
	}
}

// A reset request sealed under a token key generation this instance has not loaded is tried
// again later, never given up on.
func TestResetMailWaitsForItsTokenKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sealed, err := cryptobox.SealUnder(bytes.Repeat([]byte{9}, 32), cryptobox.ScopeToken, 2, []byte("owner@example.test"), "reset-mail:v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.auth.Queue.Insert(ctx, auth.ResetMailArgs{Email: sealed}, nil); err != nil {
		t.Fatal(err)
	}
	waitForResetJob(t, f, "retryable")
	if logged := f.log.String(); !strings.Contains(logged, "password reset request waits for its token key") || strings.Contains(logged, "could not be opened") {
		t.Fatalf("log %q", logged)
	}
}
