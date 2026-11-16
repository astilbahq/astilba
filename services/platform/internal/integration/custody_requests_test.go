package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// namedFile is the keyring file backend, naming each wrap and unwrap as a key service does. It
// can refuse unwraps, naming the refused request too.
type namedFile struct {
	*custody.File
	n      atomic.Int32
	refuse atomic.Bool
}

func (f *namedFile) Wrap(ctx context.Context, class custody.Class, key []byte, c custody.Context) ([]byte, string, error) {
	blob, _, err := f.File.Wrap(ctx, class, key, c)
	return blob, fmt.Sprintf("wrap-%d", f.n.Add(1)), err
}

func (f *namedFile) Unwrap(ctx context.Context, class custody.Class, blob []byte, c custody.Context) (custody.Result, error) {
	id := fmt.Sprintf("unwrap-%d", f.n.Add(1))
	if f.refuse.Load() {
		return custody.Result{ProviderRequestID: id}, custody.ErrUnavailable
	}
	r, err := f.File.Unwrap(ctx, class, blob, c)
	r.ProviderRequestID = id
	return r, err
}

// namedBroker is a broker on f's installation whose backend names its requests, started as serving
// does, or as an operator's command does.
func namedBroker(t *testing.T, f *fixture, operator bool) (*secretbroker.Broker, *namedFile) {
	t.Helper()
	file, err := custody.NewFile(keysOf(f.st))
	if err != nil {
		t.Fatal(err)
	}
	named := &namedFile{File: file}
	b := secretbroker.New(db.New(f.st.Pool), named, nil)
	if err = b.Install(context.Background(), f.st.Pool, uuid.Nil, operator); err != nil {
		t.Fatal(err)
	}
	return b, named
}

// accessOutcome is how the access with this ID ended, and the provider requests it names.
func accessOutcome(t *testing.T, f *fixture, id uuid.UUID) (string, []string) {
	t.Helper()
	var outcome string
	var raw []byte
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT outcome, provider_request_ids FROM secret_access_outcomes WHERE access_id=$1`, id).Scan(&outcome, &raw); err != nil {
		t.Fatal(err)
	}
	var requests []string
	if err := json.Unmarshal(raw, &requests); err != nil {
		t.Fatal(err)
	}
	return outcome, requests
}

// lastRotation is the access record of the most recent operator's rotation.
func lastRotation(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT id FROM secret_access WHERE purpose='rotate' AND principal_kind='operator' ORDER BY at DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A rewrap's outcome names the key service's request for each canary and key wrapping it moved,
// so an auditor matches the run with the provider's log. The keyring file names none.
func TestRewrapRecordsTheProvidersRequests(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ownerWorkspace(t, f)
	initKeys(t, f.st.Broker)
	if err := f.st.Rewrap(ctx, "operator@example.test", ""); err != nil {
		t.Fatal(err)
	}
	if outcome, requests := accessOutcome(t, f, lastRotation(t, f)); outcome != string(secretbroker.Used) || len(requests) != 0 {
		t.Fatalf("the keyring file's rewrap: %s %q", outcome, requests)
	}

	b, named := namedBroker(t, f, true)
	before := named.n.Load()
	rotation, err := b.BeginRotation(ctx, "operator@example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = rotation.RewrapCanaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err = rotation.RewrapWrappings(ctx); err != nil {
		t.Fatal(err)
	}
	rotation.Close(ctx, secretbroker.Used)
	finished := keyEvents(t, f, "rewrap-finished")
	moved := details(t, finished[len(finished)-1])
	var canaries, wrappings int
	if _, err = fmt.Sscan(moved["canaries"]+" "+moved["wrappings"], &canaries, &wrappings); err != nil || canaries == 0 || wrappings == 0 {
		t.Fatalf("moved %v: %v", moved, err)
	}
	// With no rewrap of its own, the backend unwraps and wraps each one again: two requests each,
	// every one of them named, in the order made.
	var want []string
	for n := before + 1; n <= named.n.Load(); n += 2 {
		want = append(want, fmt.Sprintf("unwrap-%d", n), fmt.Sprintf("wrap-%d", n+1))
	}
	outcome, requests := accessOutcome(t, f, lastRotation(t, f))
	if outcome != string(secretbroker.Used) || len(requests) != 2*(canaries+wrappings) || !slices.Equal(requests, want) {
		t.Fatalf("%s %q, want %d requests", outcome, requests, 2*(canaries+wrappings))
	}

	// A rewrap the key service refuses still names the refused request, on a failed outcome.
	named.refuse.Store(true)
	rotation, err = b.BeginRotation(ctx, "operator@example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = rotation.RewrapCanaries(ctx); !errors.Is(err, custody.ErrUnavailable) {
		t.Fatal(err)
	}
	rotation.Close(ctx, secretbroker.Failed)
	if outcome, requests = accessOutcome(t, f, lastRotation(t, f)); outcome != string(secretbroker.Failed) || !slices.Equal(requests, []string{fmt.Sprintf("unwrap-%d", named.n.Load())}) {
		t.Fatalf("%s %q", outcome, requests)
	}
}

// Each token key an instance loads is recorded before it is unwrapped, then again after with the
// key service's request, also when the unwrap failed. Those events, under the instance's access
// record, are where the requests are recorded: the record's outcome names none. The keyring file
// names none either.
func TestTokenKeyEventsNameTheProvidersRequests(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	initKeys(t, f.st.Broker)
	opened := func(instance uuid.UUID, kind string) map[string]string {
		t.Helper()
		var found []map[string]string
		for _, e := range keyEvents(t, f, kind) {
			if e.RunID == instance {
				if e.Generation.Int64 != 1 {
					t.Fatalf("%s for generation %d", kind, e.Generation.Int64)
				}
				found = append(found, details(t, e))
			}
		}
		if len(found) != 1 {
			t.Fatalf("%d %s events for instance %s", len(found), kind, instance)
		}
		return found[0]
	}

	tokens, err := f.st.Broker.Tokens(ctx, dbmigrate.CustodyStage)
	if err != nil {
		t.Fatal(err)
	}
	if d := opened(tokens.Instance(), "token-key-opened"); d["provider_requests"] != "" {
		t.Fatalf("the keyring file named a request: %v", d)
	}

	b, named := namedBroker(t, f, false)
	tokens, err = b.Tokens(ctx, dbmigrate.CustodyStage)
	if err != nil {
		t.Fatal(err)
	}
	opened(tokens.Instance(), "token-key-loaded")
	if d := opened(tokens.Instance(), "token-key-opened"); d["provider_requests"] != fmt.Sprintf("unwrap-%d", named.n.Load()) {
		t.Fatalf("token key opened %v", d)
	}
	if outcome, requests := accessOutcome(t, f, tokens.Instance()); outcome != string(secretbroker.Released) || len(requests) != 0 {
		t.Fatalf("%s %q", outcome, requests)
	}

	// An instance whose unwrap fails does not start, and records the failure with its request.
	named.refuse.Store(true)
	if _, err = b.Tokens(ctx, dbmigrate.CustodyStage); !errors.Is(err, custody.ErrUnavailable) {
		t.Fatal(err)
	}
	failed := keyEvents(t, f, "token-key-not-opened")
	if len(failed) != 1 {
		t.Fatalf("%d failures recorded", len(failed))
	}
	opened(failed[0].RunID, "token-key-loaded")
	if d := details(t, failed[0]); d["provider_requests"] != fmt.Sprintf("unwrap-%d", named.n.Load()) || !strings.Contains(d["reason"], "unavailable") {
		t.Fatalf("failure %v", d)
	}
	if outcome, _ := accessOutcome(t, f, failed[0].RunID); outcome != string(secretbroker.Failed) {
		t.Fatalf("a failed load ended %s", outcome)
	}
}
