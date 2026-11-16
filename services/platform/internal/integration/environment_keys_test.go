package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// environmentKey makes an environment's key as a release that seals under it does: a random key
// wrapped under its workspace's key at generation 1.
func environmentKey(t *testing.T, f *fixture, ws, env uuid.UUID) []byte {
	t.Helper()
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
	// The same bytes passed off as another environment's do not open.
	var other api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Another"}, 201, &other)
	environmentKey(t, f, x.ws, other.Id)
	moved, err := cryptobox.SealUnderEnvironment(key, other.Id, 1, []byte("environment-canary"), secretIdentity(x.ws, secret.Id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inspectValue(t, f.st, f.owner, x.ws, item, moved); !errors.Is(err, cryptobox.ErrKey) && !errors.Is(err, secretbroker.ErrKeyCheck) {
		t.Fatal("a value sealed under one environment's key opened as another's", err)
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

	// Recovery from the pre-purge checkpoint: its key check passes, and the purged value, which
	// the live database no longer lists, comes back erased.
	if err := portability.CheckRecoveryKeys(recoverKeys(t, f.st, x.ws), base); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `DELETE FROM secret_versions WHERE workspace_id=$1 AND id=ANY($2)`, x.ws, []uuid.UUID{secret.Id, other.Id}); err != nil {
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
	// A value under a key that lives is restored as it was, still under its environment's key.
	if err = tx.QueryRow(ctx, `SELECT encrypted_value FROM secret_versions WHERE workspace_id=$1 AND id=$2`, x.ws, other.Id).Scan(&restoredKept); err != nil || string(restoredKept) != string(keptSealed) {
		t.Fatal("the kept environment's value moved", err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1 AND state='active'", kept.Id); n != 1 {
		t.Fatal("purging one environment touched another's key")
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
