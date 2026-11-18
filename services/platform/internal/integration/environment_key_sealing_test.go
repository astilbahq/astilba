package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// Values are sealed under environment keys only while every instance seen lately opens them: an
// instance of a release before stage 5 holds sealing back until it has not been seen for five
// minutes. Each writer asks in its own transaction.
func TestEnvironmentSealingWaitsForOlderInstances(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sealable := func() bool {
		t.Helper()
		ok, err := f.st.Broker.SealsUnderEnvironments(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !sealable() {
		t.Fatal("an installation running only this release does not seal under environment keys")
	}
	older := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage) VALUES($1,4)`, older); err != nil {
		t.Fatal(err)
	}
	if sealable() {
		t.Fatal("sealed under environment keys beside an instance that cannot open them")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	if !sealable() {
		t.Fatal("an instance gone for five minutes still holds sealing back")
	}
}

// sealedHeader reads a secret version's stored header.
func sealedHeader(t *testing.T, f *fixture, ws, version uuid.UUID) (cryptobox.Header, []byte) {
	t.Helper()
	var sealed []byte
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT encrypted_value FROM secret_versions WHERE workspace_id=$1 AND id=$2`, ws, version).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	header, err := cryptobox.ReadHeader(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return header, sealed
}

// An environment's first secret makes its key, inside the transaction that stores the value and
// recorded in the key history; its values are sealed under it and open. Secrets saved at once
// through the API end up under one key.
func TestNewValuesAreSealedUnderTheirEnvironmentKey(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Sealed"}, 201, &env)
	path := x.prefix + "/" + env.Id.String()
	versions := make([]api.SecretVersion, 4)
	var wg sync.WaitGroup
	for i := range versions {
		wg.Go(func() {
			x.owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN_" + string(rune('A'+i)), "value": "concurrent-canary"}, 201, &versions[i])
		})
	}
	wg.Wait()
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1", env.Id); n != 1 {
		t.Fatalf("%d keys for one environment", n)
	}
	if created := keyEvents(t, f, "environment-key-created"); len(created) != 1 || details(t, created[0])["environment"] != env.Id.String() {
		t.Fatalf("created events %+v", created)
	}
	for _, v := range versions {
		header, sealed := sealedHeader(t, f, x.ws, v.Id)
		if header.Scope != cryptobox.ScopeEnvironment || header.Holder != env.Id || header.Generation != 1 {
			t.Fatalf("stored as %+v", header)
		}
		if value, err := inspectValue(t, f.st, f.owner, x.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: v.Id, Environment: env.Id}, sealed); err != nil || string(value) != "concurrent-canary" {
			t.Fatal(string(value), err)
		}
	}
}

// Writers making an environment's first key at once, each in its own transaction, take turns: one
// makes it, and the other seals under it.
func TestWritersMakingTheFirstKeyAtOnceShareIt(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Raced key"}, 201, &env)
	items := []secretbroker.Item{{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}, {Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}}
	sealed := make([][]byte, len(items))
	errs := make([]error, len(items))
	first, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: items})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	if sealed[0], err = keys.Seal(ctx, db.New(first), items[0], []byte("first")); err != nil {
		t.Fatal(err)
	}
	// The second writer waits for the first's key until it commits, then finds it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		second, err := f.st.Pool.Begin(ctx)
		if err != nil {
			errs[1] = err
			return
		}
		defer second.Rollback(ctx)
		if sealed[1], errs[1] = keys.Seal(ctx, db.New(second), items[1], []byte("second")); errs[1] == nil {
			errs[1] = second.Commit(ctx)
		}
	}()
	select {
	case <-done:
		t.Fatal("the second writer did not wait for the first's key", errs[1])
	case <-time.After(time.Second):
	}
	if err = first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if errs[1] != nil {
		t.Fatal(errs[1])
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1", env.Id); n != 1 {
		t.Fatalf("%d keys for one environment", n)
	}
	for i, blob := range sealed {
		header, err := cryptobox.ReadHeader(blob)
		if err != nil || header.Holder != env.Id || header.Generation != 1 {
			t.Fatalf("value %d sealed as %+v %v", i, header, err)
		}
	}
}

// While an instance of an earlier release is running, values stay under the workspace's key,
// which it opens; converting moves them later.
func TestValuesStayUnderTheWorkspaceKeyBesideOlderInstances(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage) VALUES($1,4)`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Mixed fleet"}, 201, &env)
	var secret api.SecretVersion
	x.owner.call("POST", x.prefix+"/"+env.Id.String()+"/secrets", map[string]string{"name": "TOKEN", "value": "fleet-canary"}, 201, &secret)
	if header, _ := sealedHeader(t, f, x.ws, secret.Id); header.Scope != cryptobox.ScopeWorkspace {
		t.Fatalf("stored as %+v beside an older instance", header)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1", env.Id); n != 0 {
		t.Fatal("a key was made beside an older instance")
	}
}

// A writer holding an environment's key stores its value before a purge destroys the key, and no
// value is sealed for the environment afterwards, nor a new key made for it.
func TestPurgeWaitsForAWriterAndRefusesLaterOnes(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Raced"}, 201, &env)
	path := x.prefix + "/" + env.Id.String()
	var first api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "first"}, 201, &first)
	x.lifecycle(x.owner, path, "retire", "Raced", 201)
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	writer, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	late, err := keys.Seal(ctx, db.New(writer), item, []byte("late"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.New(writer).InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: x.ws, SecretID: first.SecretId, ID: item.ID, EncryptedValue: late, AuthorID: f.owner}); err != nil {
		t.Fatal(err)
	}
	purged := make(chan api.Environment, 1)
	go func() { purged <- x.purge(x.owner, path, "Raced", []uuid.UUID{}, 201) }()
	select {
	case v := <-purged:
		t.Fatalf("purged while a writer held the key: %+v", v)
	case <-time.After(time.Second):
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if v := <-purged; v.State != api.EnvironmentStatePurged {
		t.Fatalf("purge %+v", v)
	}
	again, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Rollback(ctx)
	if _, err = keys.Seal(ctx, db.New(again), item, []byte("after")); !errors.Is(err, secretbroker.ErrKeyDestroyed) {
		t.Fatal("sealed for a purged environment", err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE environment_id=$1 AND state='active'", env.Id); n != 0 {
		t.Fatal("a purged environment has a key")
	}
	if n := x.count("SELECT count(*) FROM secret_versions WHERE id=$1 AND erased_at IS NULL", item.ID); n != 0 {
		t.Fatal("the purge kept the writer's value")
	}
}

// Values saved beside an instance of an earlier release stay under their workspace's key, and
// converting waits for that instance; once it has gone, converting moves them onto their
// environment's key and records custody stage 6.
func TestConvertMovesEnvironmentValuesOntoTheirKeys(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_stage SET stage=5`); err != nil {
		t.Fatal(err)
	}
	older := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage) VALUES($1,4)`, older); err != nil {
		t.Fatal(err)
	}
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Moved later"}, 201, &env)
	var secret api.SecretVersion
	x.owner.call("POST", x.prefix+"/"+env.Id.String()+"/secrets", map[string]string{"name": "TOKEN", "value": "moved-canary"}, 201, &secret)
	header, before := sealedHeader(t, f, x.ws, secret.Id)
	if header.Scope != cryptobox.ScopeWorkspace {
		t.Fatalf("stored as %+v beside an older instance", header)
	}
	c := convert(t, f)
	if c.Stage != 5 || !strings.Contains(c.Waiting, "stay under their workspace's key") {
		t.Fatalf("conversion beside an older instance %+v", c)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	// The command runs in its own process, which loads no token keys.
	backend, err := custody.NewFile(keysOf(f.st))
	if err != nil {
		t.Fatal(err)
	}
	command := secretbroker.New(db.New(f.st.Pool), backend, nil)
	if err = command.Install(ctx, f.st.Pool, uuid.Nil, true); err != nil {
		t.Fatal(err)
	}
	if c, err = command.Convert(ctx, "operator@example.test", "stage 6"); err != nil || c.Values == 0 || c.Stage != 6 || c.Waiting != "" {
		t.Fatalf("conversion once it had gone %+v %v", c, err)
	}
	header, after := sealedHeader(t, f, x.ws, secret.Id)
	if header.Scope != cryptobox.ScopeEnvironment || header.Holder != env.Id {
		t.Fatalf("converted to %+v", header)
	}
	var data struct {
		Data []byte `json:"data"`
	}
	var moved struct {
		Data []byte `json:"data"`
	}
	if json.Unmarshal(before, &data) != nil || json.Unmarshal(after, &moved) != nil || string(data.Data) != string(moved.Data) {
		t.Fatal("converting changed the value's data")
	}
	if value, err := inspectValue(t, f.st, f.owner, x.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: secret.Id, Environment: env.Id}, after); err != nil || string(value) != "moved-canary" {
		t.Fatal(string(value), err)
	}
}

// Stage 6 says purging reaches every copy, and corrects itself when that stops being true. An
// instance of a stage 5 release running after it is recorded may store environment values under
// their workspace's key, as rolling back and forward again does: the next check records stage 5
// again, the purge dialog stops claiming every copy, and converting moves the values and records
// stage 6 anew. Values found under a workspace key when an instance starts do the same, for an
// earlier release that ran while none of this one did.
func TestStageSixCorrectsItselfAfterARoundTrip(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	stage := func() int32 {
		t.Helper()
		s, err := db.New(f.st.Pool).CustodyStage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	reload := func() {
		t.Helper()
		if _, err := f.st.Broker.Tokens(ctx, dbmigrate.CustodyStage); err != nil {
			t.Fatal(err)
		}
	}
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Round trip"}, 201, &env)
	path := x.prefix + "/" + env.Id.String()
	everyCopy := func() bool {
		t.Helper()
		var got api.Environment
		x.owner.call("GET", path, nil, 200, &got)
		return got.PurgeReachesEveryCopy != nil && *got.PurgeReachesEveryCopy
	}
	if stage() != 6 || !everyCopy() {
		t.Fatal("a new installation does not start with every copy reached")
	}
	var secret api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "round-trip-canary"}, 201, &secret)
	// An instance of the stage 5 release runs again: that alone takes stage 6 back, before it has
	// stored anything.
	older := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage) VALUES($1,5)`, older); err != nil {
		t.Fatal(err)
	}
	reload()
	if stage() != 5 || everyCopy() {
		t.Fatal("stage 6 stayed recorded beside a stage 5 instance")
	}
	reverted := keyEvents(t, f, "stage-reverted")
	if len(reverted) != 1 || details(t, reverted[0])["stage"] != "5" || reverted[0].Reason.String != "an instance of a release before stage 6 ran since it was recorded" {
		t.Fatalf("reverted events %+v", reverted)
	}
	// It stores a value under the workspace key, as that release does.
	workspaceSealed, err := cryptobox.SealUnder(workspaceKey(t, f, x.ws, 1), cryptobox.ScopeWorkspace, 1, []byte("round-trip-canary"), secretIdentity(x.ws, secret.Id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `UPDATE secret_versions SET encrypted_value=$2 WHERE id=$1`, secret.Id, workspaceSealed); err != nil {
		t.Fatal(err)
	}
	// Once it has gone, converting moves the value and records stage 6 again.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	reload()
	if c := convert(t, f); c.Values != 1 || c.Stage != 6 || !everyCopy() {
		t.Fatalf("conversion after the round trip %+v", c)
	}
	if header, _ := sealedHeader(t, f, x.ws, secret.Id); header.Scope != cryptobox.ScopeEnvironment {
		t.Fatalf("converted to %+v", header)
	}
	// A value under a workspace key that no instance seen since stage 6 accounts for, as one an
	// earlier release stored while none of this one ran, is found when an instance starts.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE secret_versions SET encrypted_value=$2 WHERE id=$1`, secret.Id, workspaceSealed); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `DELETE FROM custody_instances WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	reload()
	if stage() != 5 || everyCopy() {
		t.Fatal("an instance starting beside a value under a workspace key kept stage 6")
	}
	if reverted = keyEvents(t, f, "stage-reverted"); len(reverted) != 2 || reverted[1].Reason.String != "environment values are under their workspace's key" {
		t.Fatalf("reverted events %+v", reverted)
	}
}

// An environment's ID is unique within its workspace only, and importing keeps it, so an import
// can bring an environment with another workspace's environment ID, by accident or on purpose.
// Each keeps its own key: both seal, and purging one leaves the other's key as it was.
func TestSealingBesideAnotherWorkspacesEnvironmentOfTheSameID(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Shared ID"}, 201, &env)
	x.owner.call("POST", x.prefix+"/"+env.Id.String()+"/secrets", map[string]string{"name": "TOKEN", "value": "original-canary"}, 201, nil)
	// Another workspace with an environment of the same ID, as an import makes it. Its key is made
	// here: the start-up key job may have run before the workspace existed.
	keysJobDone(t, f)
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
	seal := func(ws uuid.UUID) error {
		t.Helper()
		item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
		keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
		if err != nil {
			t.Fatal(err)
		}
		defer keys.Close(ctx, secretbroker.Used)
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = keys.Seal(ctx, db.New(tx), item, []byte("value")); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err = seal(other); err != nil {
		t.Fatal("the other workspace's environment:", err)
	}
	if err = seal(x.ws); err != nil {
		t.Fatal("the original environment beside the other's key:", err)
	}
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
	if err = seal(x.ws); err != nil {
		t.Fatal("the original environment after the other's purge:", err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2 AND state='active'", x.ws, env.Id); n != 1 {
		t.Fatal("purging the other workspace's environment touched the original's key")
	}
}

// A writer making an environment's first key while a purge of it is under way waits for the purge,
// then is refused, rather than failing on the key's guard.
func TestAKeyMadeWhileAPurgeIsUnderWayIsRefused(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Keyless"}, 201, &env)
	x.lifecycle(x.owner, x.prefix+"/"+env.Id.String(), "retire", "Keyless", 201)
	purge, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer purge.Rollback(ctx)
	pq := db.New(purge)
	var project uuid.UUID
	if err = purge.QueryRow(ctx, `SELECT project_id FROM environments WHERE workspace_id=$1 AND id=$2`, x.ws, env.Id).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err = pq.LockRetirementEnvironment(ctx, db.LockRetirementEnvironmentParams{WorkspaceID: x.ws, ProjectID: project, ID: env.Id}); err != nil {
		t.Fatal(err)
	}
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	sealed := make(chan error, 1)
	go func() {
		writer, err := f.st.Pool.Begin(ctx)
		if err != nil {
			sealed <- err
			return
		}
		defer writer.Rollback(ctx)
		_, err = keys.Seal(ctx, db.New(writer), item, []byte("late"))
		sealed <- err
	}()
	select {
	case err := <-sealed:
		t.Fatal("the writer did not wait for the purge", err)
	case <-time.After(time.Second):
	}
	if err = pq.EraseEnvironmentSecretValues(ctx, db.EraseEnvironmentSecretValuesParams{WorkspaceID: x.ws, EnvironmentID: env.Id}); err != nil {
		t.Fatal(err)
	}
	if err = pq.SetEnvironmentPurge(ctx, db.SetEnvironmentPurgeParams{State: "purged", PurgedBy: f.owner, WorkspaceID: x.ws, ID: env.Id}); err != nil {
		t.Fatal(err)
	}
	if err = purge.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-sealed; !errors.Is(err, secretbroker.ErrKeyDestroyed) {
		t.Fatal("a writer beside a purge", err)
	}
	if n := x.count("SELECT count(*) FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2", x.ws, env.Id); n != 0 {
		t.Fatal("a key was made for a purged environment")
	}
}

// A value a writer seals under its workspace's key, beside an instance of an earlier release, is
// counted by a conversion that records stage 6 once the instance has gone, even when the writer
// stores it after that conversion has begun counting.
func TestConvertCountsAValueStoredWhileItRecordsTheStage(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_stage SET stage=5`); err != nil {
		t.Fatal(err)
	}
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Counted"}, 201, &env)
	var first api.SecretVersion
	x.owner.call("POST", x.prefix+"/"+env.Id.String()+"/secrets", map[string]string{"name": "TOKEN", "value": "first"}, 201, &first)
	older := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage) VALUES($1,4)`, older); err != nil {
		t.Fatal(err)
	}
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	writer, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	late, err := keys.Seal(ctx, db.New(writer), item, []byte("late"))
	if err != nil {
		t.Fatal(err)
	}
	if header, err := cryptobox.ReadHeader(late); err != nil || header.Scope != cryptobox.ScopeWorkspace {
		t.Fatalf("sealed as %+v beside an older instance %v", header, err)
	}
	// The instance goes before the writer stores the value.
	if _, err = f.st.Pool.Exec(ctx, `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	converted := make(chan secretbroker.Conversion, 1)
	failed := make(chan error, 1)
	go func() {
		c, err := f.st.Broker.Convert(ctx, "operator@example.test", "stage 6")
		failed <- err
		converted <- c
	}()
	select {
	case err := <-failed:
		t.Fatalf("conversion did not wait for the writer: %+v %v", <-converted, err)
	case <-time.After(time.Second):
	}
	if _, err = db.New(writer).InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: x.ws, SecretID: first.SecretId, ID: item.ID, EncryptedValue: late, AuthorID: f.owner}); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-failed; err != nil {
		t.Fatal(err)
	}
	if c := <-converted; c.Stage != 5 || c.Waiting == "" {
		t.Fatalf("recorded beside a value under its workspace's key: %+v", c)
	}
}

// A writer reads whether to seal under environment keys when it seals, not when its transaction
// began: an instance of an earlier release that has gone by then, as converting may already have
// found when it recorded stage 6, no longer keeps the value under its workspace's key.
func TestTheGateIsReadWhenTheValueIsSealed(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Late gate"}, 201, &env)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id,stage,seen_at) VALUES($1,4,now()-interval '298 seconds')`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	writer, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	// The transaction began while the instance was seen less than five minutes ago.
	time.Sleep(3 * time.Second)
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	sealed, err := keys.Seal(ctx, db.New(writer), item, []byte("late"))
	if err != nil {
		t.Fatal(err)
	}
	if header, err := cryptobox.ReadHeader(sealed); err != nil || header.Scope != cryptobox.ScopeEnvironment {
		t.Fatalf("sealed as %+v after the instance had gone %v", header, err)
	}
}

// A session that made an environment's first key in a transaction that rolled back seals its next
// value under the key the next transaction stores, never under the one that was not.
func TestASealAfterARolledBackKeyUsesTheStoredKey(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Rolled back"}, 201, &env)
	items := []secretbroker.Item{{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}, {Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}}
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: items})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	first, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Seal(ctx, db.New(first), items[0], []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err = first.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback(ctx)
	sealed, err := keys.Seal(ctx, db.New(second), items[1], []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if value, err := inspectValue(t, f.st, f.owner, x.ws, items[1], sealed); err != nil || string(value) != "second" {
		t.Fatal("sealed under a key that was never stored", string(value), err)
	}
}

// A value stored under an environment key its own transaction made opens in that transaction,
// for a session that began before the key existed, and not outside it.
func TestAValueUnderAKeyMadeInTheTransactionOpensInIt(t *testing.T) {
	x := newRetirementFixture(t)
	f := x.f
	ctx := context.Background()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Same transaction"}, 201, &env)
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New(), Environment: env.Id}
	inspect, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close(ctx, secretbroker.Used)
	keys, err := f.st.Broker.Begin(ctx, secretbroker.Request{Workspace: x.ws, Purpose: secretbroker.Seal, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close(ctx, secretbroker.Used)
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	sealed, err := keys.Seal(ctx, db.New(tx), item, []byte("made here"))
	if err != nil {
		t.Fatal(err)
	}
	if value, err := inspect.OpenIn(ctx, db.New(tx), item, sealed); err != nil || string(value) != "made here" {
		t.Fatal(string(value), err)
	}
	if _, err = inspectValue(t, f.st, f.owner, x.ws, item, sealed); !errors.Is(err, secretbroker.ErrKeysChanged) {
		t.Fatal("opened outside the transaction that made its key", err)
	}
}
