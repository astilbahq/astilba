package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// keyEvents are the installation's key events of one kind, oldest first.
func keyEvents(t *testing.T, f *fixture, kind string) []db.InstallationKeyEvent {
	t.Helper()
	events, err := db.New(f.st.Pool).InstallationKeyEvents(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []db.InstallationKeyEvent
	for _, e := range slices.Backward(events) {
		if e.Kind == kind {
			out = append(out, db.InstallationKeyEvent(e))
		}
	}
	return out
}

// details are an event's details.
func details(t *testing.T, e db.InstallationKeyEvent) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(e.Details, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// restart is an instance starting with a keyring and a pinned installation ID.
func restart(f *fixture, keys *cryptobox.Keyring, pinned uuid.UUID) error {
	backend, err := custody.NewFile(keys)
	if err != nil {
		return err
	}
	return secretbroker.New(db.New(f.st.Pool), backend, nil).Install(context.Background(), f.st.Pool, pinned, false)
}

// The first start registers its backend and makes a canary per class, once.
func TestFirstStartRegistersBackendAndCanaries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	backends, err := db.New(f.st.Pool).CustodyBackends(ctx)
	if err != nil || len(backends) != 1 || backends[0].BackendID != "file:primary" || backends[0].Role != "read+write" {
		t.Fatalf("backends %+v: %v", backends, err)
	}
	if n := len(keyEvents(t, f, "backend-registered")); n != 1 {
		t.Fatalf("%d registrations", n)
	}
	if n := len(keyEvents(t, f, "canary-created")); n != len(custody.Classes) {
		t.Fatalf("%d canaries made", n)
	}
	var installation uuid.UUID
	if err = f.st.Pool.QueryRow(ctx, "SELECT id FROM installation").Scan(&installation); err != nil {
		t.Fatal(err)
	}
	if err = restart(f, keysOf(f.st), installation); err != nil {
		t.Fatal(err)
	}
	if len(keyEvents(t, f, "backend-registered")) != 1 || len(keyEvents(t, f, "canary-created")) != len(custody.Classes) {
		t.Fatal("a second start registered or made canaries again")
	}
}

// Start-up refuses another installation's ID, another keyring, a database copied under another
// installation ID, and a backend that is retiring, and records each refusal.
func TestStartRefusesWhatIsNotTheInstallations(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other, _ := cryptobox.New("other")
	for name, try := range map[string]struct {
		prepare func()
		keys    *cryptobox.Keyring
		pinned  uuid.UUID
		want    error
		cause   string
	}{
		"installation ID": {keys: keysOf(f.st), pinned: uuid.New(), want: secretbroker.ErrInstallationMismatch, cause: "installation-id"},
		"keyring":         {keys: other, want: secretbroker.ErrCanaryMismatch, cause: "canary"},
	} {
		before := len(keyEvents(t, f, "start-refused"))
		if err := restart(f, try.keys, try.pinned); !errors.Is(err, try.want) {
			t.Fatalf("%s: %v", name, err)
		}
		refusals := keyEvents(t, f, "start-refused")
		if len(refusals) != before+1 || details(t, refusals[len(refusals)-1])["cause"] != try.cause {
			t.Fatalf("%s: refusal not recorded: %d", name, len(refusals))
		}
	}
	// A copy of the database given another installation ID no longer opens the canaries, even
	// with the right keyring and nothing pinned.
	if _, err := f.st.Pool.Exec(ctx, "UPDATE installation SET id=gen_random_uuid()"); err != nil {
		t.Fatal(err)
	}
	if err := restart(f, keysOf(f.st), uuid.Nil); !errors.Is(err, secretbroker.ErrCanaryMismatch) {
		t.Fatalf("copied database: %v", err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE custody_backends SET role='retiring'"); err != nil {
		t.Fatal(err)
	}
	if err := restart(f, keysOf(f.st), uuid.Nil); !errors.Is(err, secretbroker.ErrBackendNotRegistered) {
		t.Fatalf("retiring backend: %v", err)
	}
}

// A rewrap records its start and finish under one run. Only the key history names the operator;
// the tenant-facing record names the run. Afterwards the canaries open without the old key.
func TestRewrapRecordsStartAndFinish(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, _, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "rewrapped-canary"}, 201, nil)
	ring := keysOf(f.st)
	rotated, _ := cryptobox.New("rotated")
	previous := ring.Active
	ring.Keys[rotated.Active] = rotated.Keys[rotated.Active]
	ring.Active = rotated.Active
	if err := f.st.Rewrap(ctx, "operator@example.test", "yearly rotation"); err != nil {
		t.Fatal(err)
	}
	started, finished := keyEvents(t, f, "rewrap-started"), keyEvents(t, f, "rewrap-finished")
	if len(started) != 1 || len(finished) != 1 || started[0].RunID != finished[0].RunID {
		t.Fatalf("started %d, finished %d", len(started), len(finished))
	}
	if started[0].Operator.String != "operator@example.test" || started[0].Reason.String != "yearly rotation" {
		t.Fatalf("start: %+v", started[0])
	}
	if d := details(t, finished[0]); d["outcome"] != "used" || d["canaries"] != "4" {
		t.Fatalf("finish: %s", finished[0].Details)
	}
	// The values' count is recorded with them, so it exists exactly when they were stored.
	if values := keyEvents(t, f, "values-rewrapped"); len(values) != 1 || values[0].RunID != started[0].RunID || details(t, values[0])["values"] != "1" {
		t.Fatalf("values: %+v", values)
	}
	var ref string
	if err := f.st.Pool.QueryRow(ctx, `SELECT operator_ref FROM secret_access WHERE principal_kind='operator'`).Scan(&ref); err != nil || ref != started[0].RunID.String() {
		t.Fatalf("tenant record names %q: %v", ref, err)
	}
	delete(ring.Keys, previous)
	if err := restart(f, ring, uuid.Nil); err != nil {
		t.Fatalf("canaries after rewrap: %v", err)
	}
	if err := f.st.Rewrap(ctx, "", ""); err == nil {
		t.Fatal("a rewrap with no operator ran")
	}
}

// Status reports the stage and what is pending, and changes nothing.
func TestCustodyStatusReportsPendingWork(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s, err := f.st.Broker.Status(ctx, f.st.Pool, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stage != dbmigrate.CustodyStage || s.Pinned || !s.Matches || s.Canaries[custody.ClassRotate] != "ok" {
		t.Fatalf("status %+v", s)
	}
	if pending := s.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "ASTILBA_INSTALLATION_ID is not set") {
		t.Fatalf("pending %q", pending)
	}
	if _, err = f.st.Pool.Exec(ctx, "DELETE FROM installation_key_canaries WHERE class='export'"); err != nil {
		t.Fatal(err)
	}
	run := uuid.New()
	if err = db.New(f.st.Pool).InsertInstallationKeyEvent(ctx, db.InsertInstallationKeyEventParams{ID: uuid.New(), RunID: run, Kind: "rewrap-started", Details: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	before := len(keyEvents(t, f, "canary-created"))
	if s, err = f.st.Broker.Status(ctx, f.st.Pool, s.Installation); err != nil {
		t.Fatal(err)
	}
	pending := strings.Join(s.Pending(), "\n")
	if !s.Pinned || s.Canaries[custody.ClassExport] != "missing" || !strings.Contains(pending, "export canary is missing") || !strings.Contains(pending, run.String()) || strings.Contains(pending, "not set") {
		t.Fatalf("pending %q", pending)
	}
	if len(keyEvents(t, f, "canary-created")) != before {
		t.Fatal("status made a canary")
	}
	// A later run that finishes supersedes one that never did.
	if err = f.st.Rewrap(ctx, "operator@example.test", ""); err != nil {
		t.Fatal(err)
	}
	if s, err = f.st.Broker.Status(ctx, f.st.Pool, s.Installation); err != nil || len(s.Unfinished) != 0 {
		t.Fatalf("unfinished after a finished run: %+v %v", s.Unfinished, err)
	}
	if s, err = f.st.Broker.Status(ctx, f.st.Pool, uuid.New()); err != nil || s.Matches || !strings.Contains(s.Pending()[0], "names another installation") {
		t.Fatalf("mismatched pin: %v", err)
	}
}

// Key events are never changed or removed, and the database dates them.
func TestKeyEventsAreAppendOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, statement := range []string{"UPDATE installation_key_events SET kind='edited'", "DELETE FROM installation_key_events"} {
		if _, err := f.st.Pool.Exec(ctx, statement); err == nil || !strings.Contains(err.Error(), "never changed or removed") {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	var late bool
	if err := f.st.Pool.QueryRow(ctx, "INSERT INTO installation_key_events(id,at,run_id,kind) VALUES(gen_random_uuid(),'2000-01-01',gen_random_uuid(),'dated') RETURNING at > now() - interval '1 minute'").Scan(&late); err != nil || !late {
		t.Fatalf("a writer dated its event: %v", err)
	}
}

// Migrating reads the stage the installation completed, from a database that records one.
func TestMigrateReadsTheRecordedStage(t *testing.T) {
	f := setup(t)
	if err := dbmigrate.Up(context.Background(), databaseDSN(f.st.Pool.Config().ConnConfig.Database)); err != nil {
		t.Fatal(err)
	}
}

// renamedBackend is the keyring under another backend ID, as a second configured backend would be.
type renamedBackend struct {
	*custody.File
	id string
}

func (r renamedBackend) ID() string { return r.id }

// Instances starting together on an installation with no backend registered agree on one: exactly
// one registers it and makes its canaries, the others start with it, and an instance configured
// with another backend is refused.
func TestConcurrentFirstStartsRegisterOneBackend(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, "DELETE FROM custody_backends"); err != nil {
		t.Fatal(err)
	}
	registered, canaries := len(keyEvents(t, f, "backend-registered")), len(keyEvents(t, f, "canary-created"))
	file, err := custody.NewFile(keysOf(f.st))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"file:primary", "file:other"}
	results := make([]error, 8)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range results {
		done.Add(1)
		go func() {
			defer done.Done()
			b := secretbroker.New(db.New(f.st.Pool), renamedBackend{file, ids[i%2]}, nil)
			start.Wait()
			results[i] = b.Install(ctx, f.st.Pool, uuid.Nil, false)
		}()
	}
	start.Done()
	done.Wait()
	backends, err := db.New(f.st.Pool).CustodyBackends(ctx)
	if err != nil || len(backends) != 1 {
		t.Fatalf("backends %+v: %v", backends, err)
	}
	for i, err := range results {
		if want := ids[i%2] == backends[0].BackendID; want && err != nil || !want && !errors.Is(err, secretbroker.ErrBackendNotRegistered) {
			t.Errorf("instance %d with %s: %v", i, ids[i%2], err)
		}
	}
	if n := len(keyEvents(t, f, "backend-registered")) - registered; n != 1 {
		t.Fatalf("%d registrations", n)
	}
	if n := len(keyEvents(t, f, "canary-created")) - canaries; n != len(custody.Classes) {
		t.Fatalf("%d canaries made", n)
	}
}

// Before a backend's first canaries, start-up opens a stored secret with the keyring, recorded in
// that secret's workspace, so a database from before canaries never pins a keyring that cannot
// read it.
func TestFirstCanariesNeedAKeyringThatOpensStoredSecrets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner, ws, path := secretEnvironment(t, f)
	owner.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "stored-before-canaries"}, 201, nil)
	if _, err := f.st.Pool.Exec(ctx, "DELETE FROM custody_backends"); err != nil {
		t.Fatal(err)
	}
	other, _ := cryptobox.New("test")
	if err := restart(f, other, uuid.Nil); !errors.Is(err, secretbroker.ErrKeyringMismatch) {
		t.Fatalf("wrong keyring: %v", err)
	}
	refusals := keyEvents(t, f, "start-refused")
	if len(refusals) == 0 || details(t, refusals[len(refusals)-1])["cause"] != "keyring" {
		t.Fatal("refusal not recorded")
	}
	var canaries int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM installation_key_canaries").Scan(&canaries); err != nil || canaries != 0 {
		t.Fatalf("a refused keyring made %d canaries: %v", canaries, err)
	}
	if err := restart(f, keysOf(f.st), uuid.Nil); err != nil {
		t.Fatalf("right keyring: %v", err)
	}
	var checks int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secret_access WHERE workspace_id=$1 AND purpose='inspect' AND job='custody-check'", ws).Scan(&checks); err != nil || checks != 2 {
		t.Fatalf("%d recorded checks: %v", checks, err)
	}
}

// Past the retention period an event keeps everything but its operator's name and reason, which
// the sweep blanks. Nothing else may change an event, blank a recent one, or remove one.
func TestKeyEventOperatorsAreBlankedPastRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(statement string, args ...any) error {
		_, err := f.st.Pool.Exec(ctx, statement, args...)
		return err
	}
	// Only the database dates events, so the test sets that aside to make old ones.
	event := func(days int) uuid.UUID {
		t.Helper()
		id := uuid.New()
		for _, err := range []error{
			exec("ALTER TABLE installation_key_events DISABLE TRIGGER installation_key_events_insert"),
			exec(`INSERT INTO installation_key_events(id,at,run_id,kind,operator,reason) VALUES($1,now()-make_interval(days => $2),gen_random_uuid(),'rewrap-started','operator@example.test','yearly rotation')`, id, days),
			exec("ALTER TABLE installation_key_events ENABLE TRIGGER installation_key_events_insert"),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	old, recent := event(401), event(399)
	for name, try := range map[string]error{
		"blanking a recent event":    exec("UPDATE installation_key_events SET operator=NULL,reason=NULL WHERE id=$1", recent),
		"blanking and changing more": exec("UPDATE installation_key_events SET operator=NULL,reason=NULL,kind='edited' WHERE id=$1", old),
		"blanking only the operator": exec("UPDATE installation_key_events SET operator=NULL WHERE id=$1", old),
		"renaming the operator":      exec("UPDATE installation_key_events SET operator='someone else' WHERE id=$1", old),
		"removing an old event":      exec("DELETE FROM installation_key_events WHERE id=$1", old),
	} {
		if try == nil || !strings.Contains(try.Error(), "never changed or removed") {
			t.Errorf("%s: %v", name, try)
		}
	}
	blanked, err := db.New(f.st.Pool).BlankKeyEventOperators(ctx, 1000)
	if err != nil || blanked != 1 {
		t.Fatalf("blanked %d: %v", blanked, err)
	}
	read := func(id uuid.UUID) (operator, reason *string, kind string) {
		t.Helper()
		if err := f.st.Pool.QueryRow(ctx, "SELECT operator,reason,kind FROM installation_key_events WHERE id=$1", id).Scan(&operator, &reason, &kind); err != nil {
			t.Fatal(err)
		}
		return
	}
	if operator, reason, kind := read(old); operator != nil || reason != nil || kind != "rewrap-started" {
		t.Fatalf("old event: %v %v %s", operator, reason, kind)
	}
	if operator, reason, _ := read(recent); operator == nil || reason == nil {
		t.Fatal("a recent event was blanked")
	}
	if blanked, err = db.New(f.st.Pool).BlankKeyEventOperators(ctx, 1000); err != nil || blanked != 0 {
		t.Fatalf("blanked again %d: %v", blanked, err)
	}
}
