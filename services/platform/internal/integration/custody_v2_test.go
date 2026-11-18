package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/recoveryrows"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// v2Header reads a stored value's header and fails unless it is v2 under scope's generation 1.
func v2Header(t *testing.T, sealed []byte, scope string) {
	t.Helper()
	header, err := cryptobox.ReadHeader(sealed)
	if err != nil || header.Version != 2 || header.Scope != scope || header.Generation != 1 {
		t.Fatalf("stored as %+v %v", header, err)
	}
}

// inspectValue opens a stored value through an inspect session of the store's broker, as a
// release reading it would.
func inspectValue(t *testing.T, st *store.Store, owner, ws uuid.UUID, item secretbroker.Item, sealed []byte) ([]byte, error) {
	t.Helper()
	s, err := st.Broker.Begin(context.Background(), secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background(), secretbroker.Used)
	return s.Open(item, sealed)
}

// asLegacy stores a workspace's only secret value again under the keyring, as a release before
// workspace keys sealed it.
func asLegacy(t *testing.T, f *fixture, ws uuid.UUID, value string) {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, `SELECT id FROM secret_versions WHERE workspace_id=$1`, ws).Scan(&id); err != nil {
		t.Fatal(err)
	}
	sealed, err := keysOf(f.st).Seal([]byte(value), secretIdentity(ws, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `UPDATE secret_versions SET encrypted_value=$2 WHERE id=$1`, id, sealed); err != nil {
		t.Fatal(err)
	}
}

// New values are written v2: a workspace's under its key, a second factor under the installation
// key. They open through the same read path the release before has, so going back to it is a
// redeploy.
func TestNewValuesAreSealedAsV2(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.execution.CreateBuildSecret(ctx, browserPrincipal(t, f.st, account, false), ws, "TOKEN", "build-v2-canary")
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err = f.st.Pool.QueryRow(ctx, `SELECT encrypted_value FROM build_secret_versions WHERE id=$1`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	v2Header(t, stored, cryptobox.ScopeWorkspace)
	item := secretbroker.Item{Kind: secretbroker.BuildSecret, ID: created.ID}
	inspect, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close(ctx, secretbroker.Used)
	if plain, err := inspect.Open(item, stored); err != nil || string(plain) != "build-v2-canary" {
		t.Fatal(string(plain), err)
	}

	b := f.browser(t)
	b.login("owner@example.test", f.password)
	mfaOwner(t, f, b)
	if account, err = f.st.Queries.AccountByID(ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	v2Header(t, account.TotpSecret, cryptobox.ScopeInstallation)
}

// lockTimedOut reports a statement refused for waiting longer than lock_timeout.
func lockTimedOut(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "55P03"
}

// A seal holds its generation until the value is stored: the generation cannot stop being sealable
// under it, and once it has, a session that began before is refused and begins again, and one that
// begins after has nothing to seal under until a new generation replaces it.
func TestSealsAreFencedOnTheirGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	item := secretbroker.Item{Kind: secretbroker.BuildSecret, ID: uuid.New()}
	begin := func() *secretbroker.Session {
		t.Helper()
		s, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(ctx, secretbroker.Used) })
		return s
	}
	early := begin()
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = early.Seal(ctx, db.New(tx), item, []byte("held")); err != nil {
		t.Fatal(err)
	}
	unseal := func(timeout string) error {
		other, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Rollback(ctx)
		if _, err = other.Exec(ctx, `SET LOCAL lock_timeout = '`+timeout+`'`); err != nil {
			t.Fatal(err)
		}
		if _, err = other.Exec(ctx, `UPDATE workspace_keys SET sealable=false WHERE workspace_id=$1 AND generation=1`, ws); err != nil {
			return err
		}
		return other.Commit(ctx)
	}
	if err = unseal("200ms"); !lockTimedOut(err) {
		t.Fatal("a generation stopped being sealable while a value sealed under it was being stored", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = unseal("5s"); err != nil {
		t.Fatal(err)
	}
	// The session holds the old generation, which the fence refuses.
	err = f.st.Transaction(ctx, func(q *db.Queries) error {
		_, err := early.Seal(ctx, q, item, []byte("late"))
		return err
	})
	if !errors.Is(err, secretbroker.ErrKeysChanged) || !secretbroker.SessionFailure(err) || early.Live() {
		t.Fatalf("seal under a generation no longer sealable: %v", err)
	}
	late := begin()
	err = f.st.Transaction(ctx, func(q *db.Queries) error {
		_, err := late.Seal(ctx, q, item, []byte("late"))
		return err
	})
	var busy *domain.Error
	if !errors.Is(err, secretbroker.ErrNoSealableKey) || !errors.As(err, &busy) || busy.Code != "temporarily_busy" {
		t.Fatalf("seal with no sealable generation: %v", err)
	}
}

// A seal waiting for its generation's lock while that generation leaves active or stops being
// sealable is refused once the change commits: it never seals under a generation that has gone.
func TestSealWaitingOnItsGenerationSeesItGo(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	keysJobDone(t, f)
	other := insertWorkspace(t, f, "Retiring")
	initKeys(t, f.st.Broker)
	for name, c := range map[string]struct {
		ws     uuid.UUID
		change string
	}{
		"unsealable": {ownerWorkspace(t, f), `UPDATE workspace_keys SET sealable=false WHERE workspace_id=$1 AND generation=1`},
		"retiring":   {other, `UPDATE workspace_keys SET state='retiring' WHERE workspace_id=$1 AND generation=1`},
	} {
		t.Run(name, func(t *testing.T) {
			item := secretbroker.Item{Kind: secretbroker.BuildSecret, ID: uuid.New()}
			s, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: c.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(ctx, secretbroker.Used)
			change, err := f.st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer change.Rollback(ctx)
			if _, err = change.Exec(ctx, c.change, c.ws); err != nil {
				t.Fatal(err)
			}
			type result struct {
				sealed []byte
				err    error
			}
			done := make(chan result, 1)
			go func() {
				var r result
				r.err = f.st.Transaction(ctx, func(q *db.Queries) error {
					var err error
					r.sealed, err = s.Seal(ctx, q, item, []byte("v"))
					return err
				})
				done <- r
			}()
			// The seal is waiting on the changed row's lock.
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				var waiting int
				if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE%'`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the seal never waited for the generation's lock")
				}
			}
			if err = change.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			r := <-done
			if !errors.Is(r.err, secretbroker.ErrKeysChanged) || r.sealed != nil {
				t.Fatalf("seal after its generation went: %v", r.err)
			}
		})
	}
}

// A workspace that an older release created without a key, after a rollback: refusing a value for
// it wakes the key job, which makes the key at once rather than at its next turn, and says nothing
// of other workspaces. Values sealed under the new key send sessions begun before it back to begin
// again.
func TestSealingWakesTheKeyJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	keysJobDone(t, f)
	ws := insertWorkspace(t, f, "Made by an older release")
	item := secretbroker.Item{Kind: secretbroker.BuildSecret, ID: uuid.New()}
	begin := func(purpose secretbroker.Purpose) *secretbroker.Session {
		t.Helper()
		s, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: purpose, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(ctx, secretbroker.Used) })
		return s
	}
	early, reader := begin(secretbroker.Seal), begin(secretbroker.Inspect)
	err := f.st.Transaction(ctx, func(q *db.Queries) error {
		_, err := early.Seal(ctx, q, item, []byte("v"))
		return err
	})
	var busy *domain.Error
	if !errors.Is(err, secretbroker.ErrKeyPending) || !errors.As(err, &busy) || busy.Code != "temporarily_busy" || errors.Is(err, secretbroker.ErrNeedKeys) {
		t.Fatalf("seal before keys: %v", err)
	}
	// The job's next turn is ten minutes away: only the wake makes the key this soon.
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		has, err := f.st.Queries.WorkspaceHasKeys(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the key job did not make the key")
		}
	}
	var sealed []byte
	if err = f.st.Transaction(ctx, func(q *db.Queries) error {
		var err error
		sealed, err = begin(secretbroker.Seal).Seal(ctx, q, item, []byte("v"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Open(item, sealed); !errors.Is(err, secretbroker.ErrNeedKeys) || reader.Live() {
		t.Fatalf("a session begun before the key: %v", err)
	}
	if plain, err := begin(secretbroker.Inspect).Open(item, sealed); err != nil || string(plain) != "v" {
		t.Fatal(string(plain), err)
	}
}

// Tokens are issued under a token key generation only once every instance seen in the last five
// minutes reports holding it. An instance that was away while issuing moved loads every
// generation before it serves, so it opens what the others issued from its first request.
func TestTokensSwitchOnceEveryInstanceHoldsTheKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	stranger := uuid.New()
	report := func(generations []int64, seen time.Duration) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,token_generations,seen_at) VALUES($1,$2,now()-$3::interval)
			ON CONFLICT (instance_id) DO UPDATE SET token_generations=EXCLUDED.token_generations, seen_at=EXCLUDED.seen_at`, stranger, generations, seen.String()); err != nil {
			t.Fatal(err)
		}
	}
	issued := func(want int) []byte {
		t.Helper()
		tokens, err := f.st.Broker.Tokens(ctx, dbmigrate.CustodyStage)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := tokens.Seal(secretbroker.DeviceToken, []byte("device"))
		if err != nil {
			t.Fatal(err)
		}
		if header, err := cryptobox.ReadHeader(sealed); err != nil || header.Version != want {
			t.Fatalf("issued %+v %v, want version %d", header, err, want)
		}
		return sealed
	}
	// An instance running now that has not loaded generation 1 holds issuing on the keyring.
	report([]int64{}, 0)
	issued(1)
	report([]int64{1}, 0)
	sealed := issued(2)
	// One unseen for longer than five minutes has stopped, and does not hold issuing back.
	report([]int64{}, 10*time.Minute)
	issued(2)
	// When it comes back, it holds every generation before its first request.
	returned, err := f.st.Broker.Tokens(ctx, dbmigrate.CustodyStage)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := returned.Open(secretbroker.DeviceToken, sealed); err != nil || string(plain) != "device" {
		t.Fatal(string(plain), err)
	}
}

// Recovery moves every value it restores onto the workspace's active generation, keeping the data,
// so no restored row is left under the keyring or a generation on its way out.
func TestRecoveryRestoresValuesUnderTheActiveGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws := ownerWorkspace(t, f)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.execution.CreateBuildSecret(ctx, browserPrincipal(t, f.st, account, false), ws, "TOKEN", "recovered-canary")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := keysOf(f.st).Seal([]byte("recovered-canary"), pipeline.EncryptionIdentity(ws, created.ID, "build-secret"))
	if err != nil {
		t.Fatal(err)
	}
	// The value as an older checkpoint holds it, under the keyring.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE build_secret_versions SET encrypted_value=$2 WHERE id=$1`, created.ID, legacy); err != nil {
		t.Fatal(err)
	}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portability.CaptureRecovery(ctx, tx, ws)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `DELETE FROM build_secret_versions WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if tx, err = f.st.Pool.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, ws), archive, f.owner); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err = tx.QueryRow(ctx, `SELECT encrypted_value FROM build_secret_versions WHERE id=$1`, created.ID).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("the merge did not restore the value")
		}
		t.Fatal(err)
	}
	v2Header(t, stored, cryptobox.ScopeWorkspace)
	// The restored row is recorded as stored, which the release before reads too.
	var row []byte
	var attributed string
	// inline-sql: the columns come from recoveryrows' compiled list
	if err = tx.QueryRow(ctx, "SELECT to_jsonb(q) FROM (SELECT "+recoveryrows.Columns("build_secret_versions")+" FROM build_secret_versions WHERE id=$1) q", created.ID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT attributed_hash FROM cross_host_recovery_rows WHERE workspace_id=$1 AND table_name='build_secret_versions'`, ws).Scan(&attributed); err != nil || attributed != recoveryrows.Hash(row) {
		t.Fatal("the restored row was not recorded as stored", err)
	}
}

// Recovery names why a restored backup cannot bring a workspace back: the backup was taken before
// the workspace was created, or before its key existed.
func TestRecoveryNamesABackupOlderThanTheWorkspace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	keysJobDone(t, f)
	keyless := insertWorkspace(t, f, "Waiting for its key")
	f.server.Close()
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.RecoverCrossHost(ctx, execution.RecoveryImport{WorkspaceID: uuid.New(), OwnerID: f.owner}); !errors.Is(err, execution.ErrRecoveryWorkspaceMissing) {
		t.Fatalf("recovering a workspace the backup lacks: %v", err)
	}
	// A capsule sealed under the key the backup lacks.
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New()}
	sealed, err := cryptobox.SealUnder(make([]byte, 32), cryptobox.ScopeWorkspace, 1, []byte("v"), secretIdentity(keyless, item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recoverKeys(t, f.st, keyless).Open(item, sealed); !errors.Is(err, secretbroker.ErrKeyNotInBackup) {
		t.Fatalf("opening a value under a key the backup lacks: %v", err)
	}
}
