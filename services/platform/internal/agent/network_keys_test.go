package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// networkKeyFile is membership id's private-link key file in dir.
func networkKeyFile(t *testing.T, dir string, id uuid.UUID) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "network", id.String()+".key"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sealedKeyFile reports whether a private-link key file holds its key sealed.
func sealedKeyFile(raw []byte) bool { return strings.HasPrefix(string(raw), "{") }

// copyStateDirectory copies every file in dir, as a backup or disk snapshot would.
func copyStateDirectory(t *testing.T, dir string) string {
	t.Helper()
	to := privateDir(t)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

// An agent that upgrades seals the private-link keys an earlier agent made in the clear, once its
// journal is sealed in the format that earlier agents refuse. The host keeps its identity.
func TestUpgradedAgentSealsItsPrivateLinkKeysInPlace(t *testing.T) {
	ctx := context.Background()
	store := InsecureKeyStore()
	for name, earlier := range map[string]func(t *testing.T, dir string){
		// An agent from before sealing left its journal and keys in the clear.
		"keyring in the clear": func(t *testing.T, dir string) {
			j, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
		},
		// An agent from before private-link keys were sealed left its keyring sealed.
		"keyring sealed": func(t *testing.T, dir string) {
			j, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := stablejson.Marshal(j.keys.Load())
			j.Close()
			sealed, err := store.Seal(ctx, journalCredentialName, raw)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := stablejson.Marshal(localStateKeys{Version: sealedStateVersion, SealedKeyring: sealed})
			if err = os.WriteFile(filepath.Join(dir, "keyring.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			earlier(t, dir)
			member := uuid.New()
			identity, err := (hostnetwork.Host{Dir: dir}).Identity(ctx, member)
			if err != nil {
				t.Fatal(err)
			}
			clear := networkKeyFile(t, dir, member)

			j, err := OpenSealed(ctx, dir, storeOpener(store))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			e := &Engine{Journal: j}
			// Opening changes no format: a command run by hand never does, and earlier agents
			// read the keys until the journal says they can't.
			if j.networkKeyStore() != nil || e.networkHost().Keys != nil || !bytes.Equal(networkKeyFile(t, dir, member), clear) {
				t.Fatal("private-link keys were sealed before the journal's format refused earlier agents")
			}
			if j.InstallStateVersion() != StateVersion {
				t.Fatal("the agent service would install a release that can't read sealed private-link keys")
			}
			if err = j.Rekey(ctx, store); err != nil {
				t.Fatal(err)
			}
			if j.StateVersion() != networkSealedStateVersion || readLocalState(t, dir).Version != networkSealedStateVersion || j.networkKeyStore() == nil {
				t.Fatal("the journal didn't reach the format that seals private-link keys")
			}

			c := &Client{Journal: j, Engine: e}
			loopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			c.keepNetworkKeysSealed(loopCtx)
			if loopCtx.Err() != nil {
				t.Fatal("the agent didn't finish sealing its private-link keys")
			}
			if !sealedKeyFile(networkKeyFile(t, dir, member)) {
				t.Fatal("the agent left its private-link key in the clear")
			}
			if again, err := e.networkHost().Identity(ctx, member); err != nil || again != identity {
				t.Fatal("sealing changed the host's identity on its network", err)
			}
			// A key made from now on is sealed when made.
			next := uuid.New()
			if _, err = e.networkHost().Identity(ctx, next); err != nil || !sealedKeyFile(networkKeyFile(t, dir, next)) {
				t.Fatal("a new private-link key was made in the clear", err)
			}
		})
	}
}

// Sealing waits for host work, which holds the network lock while it runs.
func TestNetworkKeysAreSealedOnlyBetweenCommands(t *testing.T) {
	ctx := context.Background()
	store := InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(ctx, dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	member := uuid.New()
	if _, err = (hostnetwork.Host{Dir: dir}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	e.mu.Lock()
	err = e.sealNetworkKeys(ctx, j.networkKeyStore())
	e.mu.Unlock()
	if !errors.Is(err, errEngineBusy) || sealedKeyFile(networkKeyFile(t, dir, member)) {
		t.Fatal("private-link keys were sealed while host work ran", err)
	}
	if err = e.sealNetworkKeys(ctx, j.networkKeyStore()); err != nil || !sealedKeyFile(networkKeyFile(t, dir, member)) {
		t.Fatal("private-link keys weren't sealed between commands", err)
	}
}

// A copy of the state directory, such as a backup or disk snapshot, opens no journal on another
// machine, and the private-link key in it stays as it was: sealing elsewhere neither opens nor
// replaces it. hostnetwork's tests show the key itself doesn't open there.
func TestCopiedStateDirectoryYieldsNoPrivateLinkKeyElsewhere(t *testing.T) {
	ctx := context.Background()
	store := InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(ctx, dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	member := uuid.New()
	if _, err = (&Engine{Journal: j}).networkHost().Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	j.Close()
	sealed := networkKeyFile(t, dir, member)
	if !sealedKeyFile(sealed) {
		t.Fatal("the test proves nothing: the private-link key isn't sealed")
	}
	copied := copyStateDirectory(t, dir)
	elsewhere := InsecureKeyStore()
	if j, err := OpenSealed(ctx, copied, storeOpener(elsewhere)); !errors.Is(err, ErrSealedJournal) {
		if j != nil {
			j.Close()
		}
		t.Fatal("the copied journal opened on another machine", err)
	}
	if err = (hostnetwork.Host{Dir: copied, Keys: elsewhere}).SealKeys(ctx); err != nil || !bytes.Equal(networkKeyFile(t, copied, member), sealed) {
		t.Fatal("sealing on another machine changed the copied key", err)
	}
}

// A server whose sealed key won't open, as on a copied disk, stays off the network and says why.
// It makes no key in its place: that would change its identity without its owner knowing.
func TestUnopenableNetworkKeyKeepsTheServerOffTheNetwork(t *testing.T) {
	e, host, c := networkJoinFixture(t, serveFakeNetworks(t), nil)
	host.apply = fmt.Errorf("%w: it can't read it", hostnetwork.ErrSealedKey)
	r, err := runNetworkCommand(t, e, c)
	if err != nil || r.Report.State != "needs_attention" || r.Report.Note != sealedNetworkKeyNote {
		t.Fatalf("the server didn't stay off the network with its reason: %v %s %q", err, r.Report.State, r.Report.Note)
	}
}

// A key store that isn't working only delays a network command, which runs again. Other network
// commands aren't held back: changing a link that is up needs no key store.
func TestKeyStoreThatIsNotWorkingOnlyDelaysTheCommandThatNeedsIt(t *testing.T) {
	unavailable := fmt.Errorf("%w: systemd-creds failed", hostnetwork.ErrKeyStoreUnavailable)
	for _, at := range []string{"making a key", "opening a key"} {
		t.Run(at, func(t *testing.T) {
			e, host, c := networkJoinFixture(t, serveFakeNetworks(t), nil)
			if at == "making a key" {
				host.identity = unavailable
			} else {
				host.apply = unavailable
			}
			r, err := runNetworkCommand(t, e, c)
			if !errors.Is(err, hostnetwork.ErrKeyStoreUnavailable) || r.Report.State != "running" {
				t.Fatalf("a key store not working ended the command: %v %s %q", err, r.Report.State, r.Report.Note)
			}
			if until := e.keyStoreWait.Load(); until == nil || !time.Now().Before(*until) {
				t.Fatal("the agent will call a key store that isn't working again at once")
			}
			host.identity, host.apply = nil, nil
			if r, err = runNetworkCommand(t, e, c); err != nil || r.Report.State == "running" || host.applied == 0 {
				t.Fatal("a network command that needs no key store waited for it", err, r.Report.State)
			}
		})
	}
}

// countingStore is a key store that counts the calls made to it.
type countingStore struct {
	KeyStore
	calls int
}

func (s *countingStore) Seal(ctx context.Context, name string, plain []byte) ([]byte, error) {
	s.calls++
	return s.KeyStore.Seal(ctx, name, plain)
}

func (s *countingStore) Unseal(ctx context.Context, name string, sealed []byte) ([]byte, error) {
	s.calls++
	return s.KeyStore.Unseal(ctx, name, sealed)
}

// While network commands wait for the key store, they fail at once without calling it, and that
// failure doesn't make them wait longer.
func TestNetworkCommandsLeaveTheKeyStoreAloneWhileTheyWait(t *testing.T) {
	ctx := context.Background()
	store := &countingStore{KeyStore: InsecureKeyStore()}
	j, err := OpenSealed(ctx, privateDir(t), storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := &Engine{Journal: j}
	_ = e.waitForKeyStore(hostnetwork.ErrKeyStoreUnavailable)
	until := e.keyStoreWait.Load()
	store.calls = 0
	_, err = e.networkHost().Identity(ctx, uuid.New())
	if !errors.Is(err, hostnetwork.ErrKeyStoreUnavailable) || !errors.Is(err, errWaitingForKeyStore) || store.calls != 0 {
		t.Fatalf("a network command called the key store while waiting for it: %v, %d calls", err, store.calls)
	}
	if _ = e.waitForKeyStore(err); !e.keyStoreWait.Load().Equal(*until) {
		t.Fatal("failing while waiting made the wait longer, so it could never end")
	}
	e.keyStoreWait.Store(nil)
	if _, err = e.networkHost().Identity(ctx, uuid.New()); err != nil || store.calls == 0 {
		t.Fatal("the key store wasn't used once the wait ended", err)
	}
}

// The boot restore's store opens only when a sealed key needs it, and is kept once open. One that
// doesn't open isn't tried again for every other key, and says it isn't working.
func TestOnDemandKeyStoreOpensWhenFirstUsed(t *testing.T) {
	ctx := context.Background()
	opened := 0
	keys := OnDemandKeyStore(func(context.Context) (KeyStore, error) {
		opened++
		return InsecureKeyStore(), nil
	})
	if opened != 0 {
		t.Fatal("the store was opened before it was needed")
	}
	sealed, err := keys.Seal(ctx, "name", []byte("value"))
	if err != nil || opened != 1 {
		t.Fatal(err)
	}
	if plain, err := keys.Unseal(ctx, "name", sealed); err != nil || string(plain) != "value" || opened != 1 {
		t.Fatal("the opened store wasn't kept", err)
	}
	failed := 0
	broken := OnDemandKeyStore(func(context.Context) (KeyStore, error) {
		failed++
		return nil, errors.New("the key store is unavailable")
	})
	for range 3 {
		if _, err := broken.Unseal(ctx, "name", sealed); !errors.Is(err, hostnetwork.ErrKeyStoreUnavailable) {
			t.Fatal("a store that didn't open was blamed on the value", err)
		}
	}
	if failed != 1 {
		t.Fatalf("a store that didn't open was tried %d times", failed)
	}
}

// The agent tries again a step it couldn't run: within a check when the engine was busy or the key
// store wasn't working, and an interval after one that failed.
func TestUntilDoneRetriesUntilTheStepSucceeds(t *testing.T) {
	period, check := journalRekeyInterval, journalRekeyCheck
	journalRekeyInterval, journalRekeyCheck = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { journalRekeyInterval, journalRekeyCheck = period, check })
	results := []error{errEngineBusy, errEngineBusy, hostnetwork.ErrKeyStoreUnavailable, errors.New("failed"), nil}
	var at []time.Time
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	(&Client{}).untilDone(ctx, "test", func() bool { return true }, func(context.Context) error {
		at = append(at, time.Now())
		err := results[0]
		results = results[1:]
		return err
	})
	if ctx.Err() != nil || len(at) != 5 {
		t.Fatalf("the step ran %d times before it succeeded", len(at))
	}
	for i := range 3 {
		if at[i+1].Sub(at[i]) >= journalRekeyInterval {
			t.Fatal("a busy engine or an unavailable store waited as long as a failure")
		}
	}
	if at[4].Sub(at[3]) < journalRekeyInterval {
		t.Fatal("a failed step was tried again before the interval")
	}
}

// engineCheckingStore is a key store that records each call made while the engine is held.
type engineCheckingStore struct {
	KeyStore
	e        *Engine
	underMu  int
	unlocked int
}

func (s *engineCheckingStore) note() {
	if s.e.mu.TryLock() {
		s.e.mu.Unlock()
		s.unlocked++
		return
	}
	s.underMu++
}

func (s *engineCheckingStore) Seal(ctx context.Context, name string, plain []byte) ([]byte, error) {
	s.note()
	return s.KeyStore.Seal(ctx, name, plain)
}

func (s *engineCheckingStore) Unseal(ctx context.Context, name string, sealed []byte) ([]byte, error) {
	s.note()
	return s.KeyStore.Unseal(ctx, name, sealed)
}

// Sealing keys held in the clear calls the key store only with the engine free, so no command,
// a restriction least of all, waits on the store behind it.
func TestSealingNeverHoldsTheEngineWhileCallingTheKeyStore(t *testing.T) {
	ctx := context.Background()
	dir := privateDir(t)
	j, err := OpenSealed(ctx, dir, storeOpener(InsecureKeyStore()))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	member := uuid.New()
	if _, err = (hostnetwork.Host{Dir: dir}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	store := &engineCheckingStore{KeyStore: j.KeyStore(), e: e}
	if err = e.sealNetworkKeys(ctx, store); err != nil || !sealedKeyFile(networkKeyFile(t, dir, member)) {
		t.Fatal("the key wasn't sealed", err)
	}
	if store.underMu != 0 || store.unlocked == 0 {
		t.Fatalf("the key store was called %d times with the engine held", store.underMu)
	}
}

// Removing access never waits on the journal's state lock, which a rekey holds while it calls the
// key store.
func TestNetworkKeyStoreDoesNotWaitForARekey(t *testing.T) {
	ctx := context.Background()
	j, err := OpenSealed(ctx, privateDir(t), storeOpener(InsecureKeyStore()))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	got := make(chan hostnetwork.Sealer, 1)
	go func() { got <- j.networkKeyStore() }()
	select {
	case store := <-got:
		if store == nil {
			t.Fatal("a sealed journal gave no network key store")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the network key store waited for the journal's state lock")
	}
}

// A key store call that runs out of time says the store isn't working, rather than that the value
// is bad, which would blame a key that is fine.
func TestKeyStoreTimeoutIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := capture(ctx, nil, "sleep", "5"); !errors.Is(err, hostnetwork.ErrKeyStoreUnavailable) {
		t.Fatal("a call that timed out wasn't reported as the store not working", err)
	}
}

// Each network's failure is found in a joined error, so one network's backoff never holds back
// another's restore.
func TestNetworkFailuresAreFoundPerNetwork(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	err := errors.Join(
		hostnetwork.NetworkError{NetworkID: first, Err: hostnetwork.ErrSealedKey},
		errors.Join(hostnetwork.NetworkError{NetworkID: second, Err: errWaitingForKeyStore}, errors.New("invalid network state filename")),
	)
	failed := networkFailures(err)
	if len(failed) != 2 || !errors.Is(failed[first], hostnetwork.ErrSealedKey) || !errors.Is(failed[second], hostnetwork.ErrKeyStoreUnavailable) {
		t.Fatalf("network failures weren't found per network: %v", failed)
	}
	if len(networkFailures(nil)) != 0 {
		t.Fatal("no error found failures")
	}
}

// A network whose key isn't this machine's waits an hour before it is tried again, while its
// intent stays the one that failed. Any other failure, such as a restriction left half applied, is
// tried again at the next check, and one network waiting never holds back another.
func TestRestoreRetriesWaitOnlyForAKeyThatIsNotThisMachines(t *testing.T) {
	now := time.Now()
	foreign, halfApplied, unavailable, restored, untried := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	retry := map[uuid.UUID]restoreWait{restored: {now.Add(-time.Minute), "r"}, untried: {now.Add(time.Hour), "u"}}
	tried := map[uuid.UUID]string{foreign: "f", halfApplied: "h", unavailable: "a", restored: "r"}
	failed := map[uuid.UUID]error{
		foreign:     fmt.Errorf("%w: sealed elsewhere", hostnetwork.ErrSealedKey),
		halfApplied: errors.New("managed network nft operation failed"),
		unavailable: errWaitingForKeyStore,
	}
	restoreRetries(retry, tried, failed, now)
	if w := retry[foreign]; !w.until.Equal(now.Add(journalRekeyInterval)) || w.intent != "f" {
		t.Fatal("a key that isn't this machine's is tried again before the hour")
	}
	for _, id := range []uuid.UUID{halfApplied, unavailable, restored} {
		if _, waiting := retry[id]; waiting {
			t.Fatal("a network that can be restored soon waits")
		}
	}
	if w := retry[untried]; !w.until.Equal(now.Add(time.Hour)) {
		t.Fatal("a network not tried lost its wait")
	}
}
