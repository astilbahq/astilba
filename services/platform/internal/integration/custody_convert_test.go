package integration

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/recoveryrows"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// legacySeed is one stored value sealed under the installation keyring, as a release before
// workspace keys stored it.
type legacySeed struct {
	item  secretbroker.Item
	ws    uuid.UUID
	plain string
	// read selects the stored value, given the item's ID.
	read string
}

// seedLegacyValues stores one value of every kind conversion moves under the keyring: one in each
// workspace table, and the owner's active and pending second factors. The installation starts at
// stage 3, as one upgraded from the release before.
func seedLegacyValues(t *testing.T, f *fixture) (uuid.UUID, []legacySeed) {
	t.Helper()
	ctx := context.Background()
	keysJobDone(t, f)
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "legacy-secret"}, 201, nil)
	asLegacy(t, f, ws, "legacy-secret")
	var secret, env uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, `SELECT v.id,s.environment_id FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE v.workspace_id=$1`, ws).Scan(&secret, &env); err != nil {
		t.Fatal(err)
	}
	server := uuid.New()
	ring := keysOf(f.st)
	seal := func(plain, identity string) []byte {
		t.Helper()
		sealed, err := ring.Seal([]byte(plain), identity)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	seeds := []legacySeed{{item: secretbroker.Item{Kind: secretbroker.SecretVersion, ID: secret}, ws: ws, plain: "legacy-secret", read: `SELECT encrypted_value FROM secret_versions WHERE id=$1`}}
	integrations := []struct {
		kind   secretbroker.Kind
		insert string
		read   string
	}{
		{secretbroker.Git, `INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,'{}',$3)`, `SELECT encrypted_access FROM git_connections WHERE id=$1`},
		{secretbroker.GitHubAuthorization, `INSERT INTO github_app_authorizations(id,workspace_id,account_id,auth_version,session_hash,state_hash,app_id,encrypted_access,status,expires_at) VALUES($1,$2,$4,1,'\x00',$5,1,$3,'pending',now()+interval '1 hour')`, `SELECT encrypted_access FROM github_app_authorizations WHERE id=$1`},
		{secretbroker.Registry, `INSERT INTO registries(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,'{}',$3)`, `SELECT encrypted_access FROM registries WHERE id=$1`},
		{secretbroker.BuildSecret, `INSERT INTO build_secret_versions(id,workspace_id,name,encrypted_value) VALUES($1,$2,'LEGACY',$3)`, `SELECT encrypted_value FROM build_secret_versions WHERE id=$1`},
		{secretbroker.BackupDestination, `INSERT INTO backup_destinations(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,'{}',$3)`, `SELECT encrypted_access FROM backup_destinations WHERE id=$1`},
		{secretbroker.BackupPolicy, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status) VALUES($1,$2,$4,$5,'{}',$3,'{}')`, `SELECT encrypted_access FROM backup_policies WHERE id=$1`},
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Legacy backups','deployment')`, server, ws); err != nil {
		t.Fatal(err)
	}
	for _, i := range integrations {
		id := uuid.New()
		plain := "legacy-" + string(i.kind)
		args := []any{id, ws, seal(plain, pipeline.EncryptionIdentity(ws, id, string(i.kind)))}
		switch i.kind {
		case secretbroker.GitHubAuthorization:
			args = append(args, f.owner, id[:])
		case secretbroker.BackupPolicy:
			args = append(args, env, server)
		}
		if _, err := f.st.Pool.Exec(ctx, i.insert, args...); err != nil {
			t.Fatal(i.kind, err)
		}
		seeds = append(seeds, legacySeed{item: secretbroker.Item{Kind: i.kind, ID: id}, ws: ws, plain: plain, read: i.read})
	}
	webhook := seal("legacy-webhook", "astilba/alert-webhook/"+ws.String())
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO alert_settings(workspace_id,encrypted_webhook) VALUES($1,$2) ON CONFLICT (workspace_id) DO UPDATE SET encrypted_webhook=EXCLUDED.encrypted_webhook`, ws, webhook); err != nil {
		t.Fatal(err)
	}
	seeds = append(seeds, legacySeed{item: secretbroker.Item{Kind: secretbroker.AlertWebhook, ID: ws}, ws: ws, plain: "legacy-webhook", read: `SELECT encrypted_webhook FROM alert_settings WHERE workspace_id=$1`})
	totp, pending := seal("legacy-totp", "totp:v1:"+f.owner.String()), seal("legacy-pending", "totp-pending:v1:"+f.owner.String())
	if _, err := f.st.Pool.Exec(ctx, `UPDATE accounts SET totp_secret=$2,totp_pending=$3 WHERE id=$1`, f.owner, totp, pending); err != nil {
		t.Fatal(err)
	}
	seeds = append(seeds,
		legacySeed{item: secretbroker.Item{Kind: secretbroker.TOTP, ID: f.owner}, plain: "legacy-totp", read: `SELECT totp_secret FROM accounts WHERE id=$1`},
		legacySeed{item: secretbroker.Item{Kind: secretbroker.TOTPPending, ID: f.owner}, plain: "legacy-pending", read: `SELECT totp_pending FROM accounts WHERE id=$1`})
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_stage SET stage=3`); err != nil {
		t.Fatal(err)
	}
	return ws, seeds
}

func (s legacySeed) stored(t *testing.T, f *fixture) []byte {
	t.Helper()
	var sealed []byte
	if err := f.st.Pool.QueryRow(context.Background(), s.read, s.item.ID).Scan(&sealed); err != nil {
		t.Fatal(s.item.Kind, err)
	}
	return sealed
}

// open opens the stored value as its readers do: a workspace's through a recover session, which
// may open every kind, a second factor through its account's sign-in.
func (s legacySeed) open(t *testing.T, f *fixture) string {
	t.Helper()
	ctx := context.Background()
	r := secretbroker.Request{Workspace: s.ws, Purpose: secretbroker.Recover, Principal: secretbroker.Operator("test-open"), Kinds: secretbroker.RecoveryKinds}
	if s.ws == uuid.Nil {
		r = secretbroker.Request{Purpose: secretbroker.Authenticate, Principal: secretbroker.Account(f.owner), Items: []secretbroker.Item{s.item}}
	}
	session, err := f.st.Broker.Begin(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx, secretbroker.Used)
	plain, err := session.Open(s.item, s.stored(t, f))
	if err != nil {
		t.Fatal(s.item.Kind, err)
	}
	return string(plain)
}

// envelopeData is a sealed value's data ciphertext, which converting keeps.
func envelopeData(t *testing.T, sealed []byte) []byte {
	t.Helper()
	var envelope struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(sealed, &envelope); err != nil || len(envelope.Data) == 0 {
		t.Fatal("not an envelope", err)
	}
	return envelope.Data
}

func convert(t *testing.T, f *fixture) secretbroker.Conversion {
	t.Helper()
	c, err := f.st.Broker.Convert(context.Background(), "operator@example.test", "stage 4")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func remaining(c secretbroker.Conversion) (n int64) {
	for _, r := range c.Remaining {
		n += r.Remaining
	}
	return n
}

// Converting moves every value of every kind off the installation keyring, onto its workspace's
// key or the installation key, and keeps its data, so recovery data captured before still merges.
// It records what it did, and the stage once nothing is left; running it again changes nothing.
func TestConvertMovesEveryValueOntoItsHoldersKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, seeds := seedLegacyValues(t, f)
	data := map[secretbroker.Kind][]byte{}
	for _, s := range seeds {
		data[s.item.Kind] = envelopeData(t, s.stored(t, f))
	}
	status, err := f.st.Broker.Status(ctx, f.st.Pool, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if pending := strings.Join(status.Pending(), "\n"); !strings.Contains(pending, "10 values are still sealed under the installation keyring") {
		t.Fatalf("pending %q", pending)
	}
	base := capturedBase(t, f, ws)
	restored := capturedBase(t, f, ws)
	restoredID := restoredAs(t, f, ws, restored)
	c := convert(t, f)
	if c.Values != int64(len(seeds)) || c.Raced != 0 || len(c.Skipped) != 0 || remaining(c) != 0 || c.Stage != 4 || c.Waiting != "" {
		t.Fatalf("conversion %+v", c)
	}
	for _, s := range seeds {
		sealed := s.stored(t, f)
		scope := cryptobox.ScopeWorkspace
		if s.ws == uuid.Nil {
			scope = cryptobox.ScopeInstallation
		}
		v2Header(t, sealed, scope)
		if !bytes.Equal(envelopeData(t, sealed), data[s.item.Kind]) {
			t.Errorf("%s: converting changed the data", s.item.Kind)
		}
		if plain := s.open(t, f); plain != s.plain {
			t.Errorf("%s opened to %q", s.item.Kind, plain)
		}
	}
	started, finished := keyEvents(t, f, "convert-started"), keyEvents(t, f, "convert-finished")
	if len(started) != 1 || len(finished) != 1 || started[0].RunID != c.Run || finished[0].RunID != c.Run || started[0].Operator.String != "operator@example.test" {
		t.Fatalf("started %+v, finished %+v", started, finished)
	}
	if d := details(t, finished[0]); d["outcome"] != "used" || d["values"] != "10" || d["remaining"] != "0" {
		t.Fatalf("finish %s", finished[0].Details)
	}
	var moved int
	for _, e := range keyEvents(t, f, "values-converted") {
		if e.RunID == c.Run {
			moved++
		}
	}
	if moved != 2 {
		t.Fatalf("%d batches recorded, want the installation's and the workspace's", moved)
	}
	if stages := keyEvents(t, f, "stage-completed"); len(stages) == 0 || details(t, stages[len(stages)-1])["stage"] != "4" {
		t.Fatal("stage 4 was not recorded")
	}
	// Tenants' records name the run, never the operator.
	var records int
	if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM secret_access WHERE workspace_id=$1 AND purpose='rotate' AND operator_ref=$2 AND reason='convert'`, ws, c.Run.String()).Scan(&records); err != nil || records != 1 {
		t.Fatal(records, err)
	}
	// Recovery data from before still merges: its immutable rows compare by content, and the
	// record of a row a recovery restored was moved to its content form before the row's key moved.
	var attributed string
	if err = f.st.Pool.QueryRow(ctx, `SELECT attributed_hash FROM cross_host_recovery_rows WHERE workspace_id=$1 AND table_name='secret_versions'`, ws).Scan(&attributed); err != nil || attributed != recoveryrows.ContentHash("secret_versions", storedRow(t, f, "secret_versions", restoredID)) {
		t.Fatal("the restored row's record was not moved to its content form", err)
	}
	if err = mergeBase(t, f, ws, base); err != nil {
		t.Fatal("a recovery base captured before converting:", err)
	}
	if err = mergeBase(t, f, ws, restored); err != nil {
		t.Fatal("a recovery base a restored row came from:", err)
	}
	again := convert(t, f)
	if again.Values != 0 || again.Stage != 4 || remaining(again) != 0 {
		t.Fatalf("second run %+v", again)
	}
}

// Recovery checkpoints are never converted: hosts hold them bound by digest, and one sealed under
// the keyring still opens afterwards.
func TestConvertLeavesCheckpointsUnderTheKeyring(t *testing.T) {
	f := setup(t)
	ws, _ := seedLegacyValues(t, f)
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: uuid.New()}
	// A capsule as a host holds it: under the keyring, with an identity the live rows never use.
	capsule, err := keysOf(f.st).Seal([]byte("held-by-a-host"), secretIdentity(ws, item.ID))
	if err != nil {
		t.Fatal(err)
	}
	convert(t, f)
	if plain, err := recoverKeys(t, f.st, ws).Open(item, capsule); err != nil || string(plain) != "held-by-a-host" {
		t.Fatal(string(plain), err)
	}
}

// A value written while conversion holds it is kept, not overwritten with the converted old one; the
// run says so and leaves the stage, and the next run converts it.
func TestConvertKeepsAValueWrittenWhileItRuns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, seeds := seedLegacyValues(t, f)
	secret := seeds[0]
	writer, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	newer, err := keysOf(f.st).Seal([]byte("written-meanwhile"), secretIdentity(ws, secret.item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `UPDATE secret_versions SET encrypted_value=$2 WHERE id=$1`, secret.item.ID, newer); err != nil {
		t.Fatal(err)
	}
	done := make(chan secretbroker.Conversion, 1)
	go func() {
		c, _ := f.st.Broker.Convert(ctx, "operator@example.test", "")
		done <- c
	}()
	waitForLock(t, f, "UPDATE secret_versions")
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	c := <-done
	if c.Raced != 1 || remaining(c) != 1 || c.Stage != 3 || !strings.Contains(c.Waiting, "1 values are still sealed") {
		t.Fatalf("conversion %+v", c)
	}
	if !bytes.Equal(secret.stored(t, f), newer) {
		t.Fatal("the value written meanwhile was overwritten")
	}
	if c = convert(t, f); c.Values != 1 || c.Stage != 4 {
		t.Fatalf("second run %+v", c)
	}
	secret.plain = "written-meanwhile"
	if plain := secret.open(t, f); plain != secret.plain {
		t.Fatal(plain)
	}
}

// waitForLock waits until a statement starting with prefix is waiting on a lock.
func waitForLock(t *testing.T, f *fixture, prefix string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var waiting int
		if err := f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, "%"+prefix+"%").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing waited on a lock for %q", prefix)
		}
	}
}

// A run that stops part way keeps the batches it stored, and the next run finishes the rest.
func TestConvertResumesARunThatStopped(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _ := seedLegacyValues(t, f)
	var env uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, `SELECT environment_id FROM secrets WHERE workspace_id=$1`, ws).Scan(&env); err != nil {
		t.Fatal(err)
	}
	// More than a batch of secret versions, the last of which cannot be opened: the run stops on it,
	// after storing the batches before.
	ring := keysOf(f.st)
	var broken uuid.UUID
	for i := range 150 {
		secret, version := uuid.New(), uuid.New()
		identity := secretIdentity(ws, version)
		if i == 149 {
			version, broken = uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff"), version
			identity = secretIdentity(ws, broken)
		}
		sealed, err := ring.Seal([]byte("bulk"), identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO secrets(workspace_id,environment_id,id,name) VALUES($1,$2,$3,$4)`, ws, env, secret, "BULK_"+secret.String()[:8]); err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO secret_versions(workspace_id,secret_id,id,encrypted_value,author_id) VALUES($1,$2,$3,$4,$5)`, ws, secret, version, sealed, f.owner); err != nil {
			t.Fatal(err)
		}
	}
	last := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	c, err := f.st.Broker.Convert(ctx, "operator@example.test", "")
	if err == nil || !strings.Contains(err.Error(), last.String()) || !strings.Contains(err.Error(), ws.String()) {
		t.Fatalf("a run that met a value it cannot open: %v", err)
	}
	if c.Values < 100 || c.Stage != 0 {
		t.Fatalf("the run stored %d values before it stopped", c.Values)
	}
	if d := details(t, keyEvents(t, f, "convert-finished")[0]); d["outcome"] != "failed" {
		t.Fatalf("finish %v", d)
	}
	if _, err = f.st.Pool.Exec(ctx, `DELETE FROM secret_versions WHERE id=$1`, last); err != nil {
		t.Fatal(err)
	}
	rest := convert(t, f)
	if rest.Values+c.Values != 10+149 || rest.Stage != 4 {
		t.Fatalf("first run %d, second %+v", c.Values, rest)
	}
}

// A holder another run holds, or with no generation to seal under, is skipped and named, and the
// stage waits for it. A generation that stops being sealable mid-batch refuses that batch.
func TestConvertSkipsHoldersItCannotConvert(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, seeds := seedLegacyValues(t, f)
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	name := "workspace:" + ws.String()
	if locked, err := db.New(conn).TryLockKeyCreation(ctx, name); err != nil || !locked {
		t.Fatal(locked, err)
	}
	c := convert(t, f)
	if len(c.Skipped) != 1 || !strings.Contains(c.Skipped[0], name) || c.Stage != 3 || remaining(c) != int64(len(seeds)-2) {
		t.Fatalf("conversion %+v", c)
	}
	if _, err = db.New(conn).UnlockKeyCreation(ctx, name); err != nil {
		t.Fatal(err)
	}
	// The workspace's generation stops being sealable while a batch waits to hold it.
	change, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Rollback(ctx)
	if _, err = change.Exec(ctx, `UPDATE workspace_keys SET sealable=false WHERE workspace_id=$1 AND generation=1`, ws); err != nil {
		t.Fatal(err)
	}
	done := make(chan secretbroker.Conversion, 1)
	go func() { c, _ := f.st.Broker.Convert(ctx, "operator@example.test", ""); done <- c }()
	waitForLock(t, f, "FOR SHARE")
	if err = change.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	c = <-done
	if len(c.Skipped) != 1 || !strings.Contains(c.Skipped[0], name) || c.Values != 0 || c.Stage != 3 {
		t.Fatalf("conversion with no sealable generation %+v", c)
	}
	for _, s := range seeds[:len(seeds)-2] {
		if header, err := cryptobox.ReadHeader(s.stored(t, f)); err != nil || header.Version != 1 {
			t.Fatalf("%s moved onto a generation that was going: %+v %v", s.item.Kind, header, err)
		}
	}
}

// The stage waits for every instance seen lately to run a release that never seals under the
// keyring; one reporting no stage may.
func TestConvertWaitsForOlderInstances(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	seedLegacyValues(t, f)
	older := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO custody_instances(instance_id) VALUES($1)`, older); err != nil {
		t.Fatal(err)
	}
	c := convert(t, f)
	if remaining(c) != 0 || c.Stage != 3 || !strings.Contains(c.Waiting, "1 instances seen in the last five minutes") {
		t.Fatalf("conversion %+v", c)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
		t.Fatal(err)
	}
	if c = convert(t, f); c.Stage != 4 {
		t.Fatalf("conversion once it stopped %+v", c)
	}
	// An instance of this release reports the stage it completes.
	var reported int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM custody_instances WHERE stage=4`).Scan(&reported); err != nil || reported == 0 {
		t.Fatal(reported, err)
	}
}

// Recording the stage holds every table with sealed values against writes, so none lands between
// the check and the record. A value an older release stores after it is still found: status names
// it, the check whatever stops reading the keyring runs refuses, and converting again moves it.
func TestConversionCheckCatchesLaterLegacyValues(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _ := seedLegacyValues(t, f)
	convert(t, f)
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if waiting, err := f.st.Broker.CheckConverted(ctx, db.New(tx)); err != nil || waiting != "" {
		t.Fatal(waiting, err)
	}
	other, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(ctx, `SET LOCAL lock_timeout='200ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(ctx, `UPDATE secret_versions SET author_id=author_id WHERE workspace_id=$1`, ws); !lockTimedOut(err) {
		t.Fatal("a value was written while the check held the tables", err)
	}
	other.Rollback(ctx)
	tx.Rollback(ctx)
	// An older release, started after the stage was recorded, stores a value under the keyring.
	asLegacy(t, f, ws, "after-stage-4")
	status, err := f.st.Broker.Status(ctx, f.st.Pool, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if pending := strings.Join(status.Pending(), "\n"); status.Stage != 4 || !strings.Contains(pending, "1 values are sealed under the installation keyring although they were converted") {
		t.Fatalf("pending %q", pending)
	}
	if tx, err = f.st.Pool.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if waiting, err := f.st.Broker.CheckConverted(ctx, db.New(tx)); err != nil || !strings.Contains(waiting, "1 values are still sealed") {
		t.Fatal(waiting, err)
	}
	tx.Rollback(ctx)
	if c := convert(t, f); c.Values != 1 || c.Stage != 4 || remaining(c) != 0 {
		t.Fatalf("conversion %+v", c)
	}
}
