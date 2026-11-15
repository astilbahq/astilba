package agent

import (
	"bytes"
	"context"
	"errors"
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

// A copy of the state directory, such as a backup or disk snapshot, opens neither the journal nor
// any private-link key on another machine: not through the agent, nor through the boot-time
// restore.
func TestCopiedStateDirectoryYieldsNoPrivateLinkKeyElsewhere(t *testing.T) {
	ctx := context.Background()
	store := InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(ctx, dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	member := uuid.New()
	if _, err = e.networkHost().Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	j.Close()
	sealed := networkKeyFile(t, dir, member)
	if !sealedKeyFile(sealed) {
		t.Fatal("the test proves nothing: the private-link key isn't sealed")
	}
	// Its own machine opens the key, as the boot-time restore does.
	if _, err = (hostnetwork.Host{Dir: dir, Keys: OnDemandKeyStore(storeOpener(store))}).Identity(ctx, member); err != nil {
		t.Fatal("the restore couldn't open its own machine's key", err)
	}

	copied := copyStateDirectory(t, dir)
	elsewhere := InsecureKeyStore()
	if j, err := OpenSealed(ctx, copied, storeOpener(elsewhere)); !errors.Is(err, ErrSealedJournal) {
		if j != nil {
			j.Close()
		}
		t.Fatal("the copied journal opened on another machine", err)
	}
	for name, keys := range map[string]hostnetwork.Sealer{
		"the agent's store":   elsewhere,
		"the restore's store": OnDemandKeyStore(storeOpener(elsewhere)),
		"no store":            nil,
	} {
		if _, err := (hostnetwork.Host{Dir: copied, Keys: keys}).Identity(ctx, member); !errors.Is(err, hostnetwork.ErrSealedKey) {
			t.Fatalf("%s: the copied private-link key opened on another machine: %v", name, err)
		}
	}
	if !bytes.Equal(networkKeyFile(t, copied, member), sealed) {
		t.Fatal("a key that couldn't be opened was replaced")
	}
}

// The restore opens the key store only for a key that is sealed, so a host whose store is
// unavailable still restores keys held in the clear.
func TestRestoreOpensTheKeyStoreOnlyForASealedKey(t *testing.T) {
	ctx := context.Background()
	opened := 0
	keys := OnDemandKeyStore(func(context.Context) (KeyStore, error) {
		opened++
		return nil, errors.New("the key store is unavailable")
	})
	dir := privateDir(t)
	member := uuid.New()
	identity, err := (hostnetwork.Host{Dir: dir}).Identity(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := (hostnetwork.Host{Dir: dir, Keys: keys}).Identity(ctx, member); err != nil || again != identity {
		t.Fatal("a key held in the clear needed the key store", err)
	}
	if opened != 0 {
		t.Fatal("the key store was opened for nothing sealed")
	}
}
