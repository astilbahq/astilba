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
// It makes no key in its place: that would change its identity without its owner knowing. A key
// store that isn't working yet only delays the command, which runs again.
func TestUnopenableNetworkKeyKeepsTheServerOffTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cause    error
		finished bool
	}{
		{"not this machine's key", hostnetwork.ErrSealedKey, true},
		{"store not working", hostnetwork.ErrKeyStoreUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, host, c := networkJoinFixture(t, serveFakeNetworks(t), nil)
			host.apply = fmt.Errorf("%w: it can't read it", tc.cause)
			r, err := runNetworkCommand(t, e, c)
			if tc.finished {
				if err != nil || r.Report.State != "needs_attention" || r.Report.Note != sealedNetworkKeyNote {
					t.Fatalf("the server didn't stay off the network with its reason: %v %s %q", err, r.Report.State, r.Report.Note)
				}
				return
			}
			if !errors.Is(err, hostnetwork.ErrKeyStoreUnavailable) || r.Report.State != "running" {
				t.Fatalf("a key store not working yet ended the command: %v %s %q", err, r.Report.State, r.Report.Note)
			}
		})
	}
}

// The boot restore's store opens only when a sealed key needs it, is kept once open, and is opened
// again after a failure.
func TestOnDemandKeyStoreOpensWhenFirstUsed(t *testing.T) {
	ctx := context.Background()
	opened, fail := 0, true
	store := InsecureKeyStore()
	keys := OnDemandKeyStore(func(context.Context) (KeyStore, error) {
		opened++
		if fail {
			return nil, errors.New("the key store is unavailable")
		}
		return store, nil
	})
	if _, err := keys.Seal(ctx, "name", []byte("value")); err == nil || opened != 1 {
		t.Fatal("an unavailable store sealed", err)
	}
	fail = false
	sealed, err := keys.Seal(ctx, "name", []byte("value"))
	if err != nil || opened != 2 {
		t.Fatal("the store wasn't opened again after failing", err)
	}
	if plain, err := keys.Unseal(ctx, "name", sealed); err != nil || string(plain) != "value" || opened != 2 {
		t.Fatal("the opened store wasn't kept", err)
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
