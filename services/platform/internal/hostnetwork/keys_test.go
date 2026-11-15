package hostnetwork

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// machine seals to a key only it holds, as one machine's key store does. Like the agent's, it
// refuses what another machine sealed, or what was sealed under another name.
type machine struct{ aead cipher.AEAD }

func newMachine(t *testing.T) machine {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return machine{aead}
}

func (m machine) Seal(_ context.Context, name string, plain []byte) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return m.aead.Seal(nonce, nonce, plain, []byte(name)), nil
}

func (m machine) Unseal(_ context.Context, name string, sealed []byte) ([]byte, error) {
	if len(sealed) < m.aead.NonceSize() {
		return nil, errors.New("sealed elsewhere")
	}
	return m.aead.Open(nil, sealed[:m.aead.NonceSize()], sealed[m.aead.NonceSize():], []byte(name))
}

// unsealable fails every call, as a store that can't seal now does.
type unsealable struct{}

func (unsealable) Seal(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("store unavailable")
}
func (unsealable) Unseal(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("store unavailable")
}

// copyState copies a state directory's network files, as a backup or disk snapshot would.
func copyState(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(to, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(from, "network"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(from, "network", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(to, "network", entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return to
}

// privateKeyOf reads membership id's key as this host holds it, for checks on what files hold.
func privateKeyOf(t *testing.T, h Host, id uuid.UUID) []byte {
	t.Helper()
	key, err := h.privateKey(context.Background(), id, false)
	if err != nil {
		t.Fatal(err)
	}
	return key.Bytes()
}

// holdsKey reports whether any file in dir holds key in the clear, raw or as base64.
func holdsKey(t *testing.T, dir string, key []byte) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found = found || bytes.Contains(raw, key) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(key)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestCopiedStateYieldsNoPrivateLinkKeyOnAnotherMachine(t *testing.T) {
	ctx := context.Background()
	here := Host{Dir: t.TempDir(), Keys: newMachine(t)}
	member := uuid.New()
	identity, err := here.Identity(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	again, err := here.Identity(ctx, member)
	if err != nil || again != identity {
		t.Fatal("a sealed key didn't open on its own machine", err)
	}
	key := privateKeyOf(t, here, member)
	if holdsKey(t, here.Dir, key) {
		t.Fatal("the state directory holds the private-link key in the clear")
	}
	path := filepath.Join(here.Dir, "network", member.String()+".key")
	sealed, _ := os.ReadFile(path)
	// An agent from before sealing reads the file as a key in the clear and refuses it.
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sealed))); err == nil {
		t.Fatal("an earlier agent would read the sealed key file as a key")
	}

	copied := copyState(t, here.Dir)
	for name, elsewhere := range map[string]Host{
		"another machine": {Dir: copied, Keys: newMachine(t)},
		"no key store":    {Dir: copied},
	} {
		if _, err := elsewhere.Identity(ctx, member); !errors.Is(err, ErrSealedKey) {
			t.Fatalf("%s: the copied key opened or failed otherwise: %v", name, err)
		}
		if _, err := elsewhere.privateKey(ctx, member, false); !errors.Is(err, ErrSealedKey) {
			t.Fatalf("%s: the copied key opened for an apply: %v", name, err)
		}
		if elsewhere.Keys != nil {
			if err := elsewhere.SealKeys(ctx); !errors.Is(err, ErrSealedKey) {
				t.Fatalf("%s: sealing passed over a key it can't read: %v", name, err)
			}
		}
		// Nothing replaced the key that can't be read, which would give the server a new identity.
		if raw, _ := os.ReadFile(filepath.Join(copied, "network", member.String()+".key")); !bytes.Equal(raw, sealed) {
			t.Fatalf("%s: the copied key file changed", name)
		}
	}
}

func TestSealedKeyOpensOnlyForItsMembership(t *testing.T) {
	ctx := context.Background()
	h := Host{Dir: t.TempDir(), Keys: newMachine(t)}
	first, second := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{first, second} {
		if _, err := h.Identity(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(h.Dir, "network", first.String()+".key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.Dir, "network", second.String()+".key"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Identity(ctx, second); !errors.Is(err, ErrSealedKey) {
		t.Fatal("one membership's sealed key opened as another's", err)
	}
}

func TestSealKeysUpgradesKeysHeldInTheClear(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// An earlier agent made these keys in the clear, and an apply it cut short left its
	// WireGuard configuration behind.
	clear := Host{Dir: dir}
	members := []uuid.UUID{uuid.New(), uuid.New()}
	identities := map[uuid.UUID]Identity{}
	for _, id := range members {
		identity, err := clear.Identity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		identities[id] = identity
	}
	keys := [][]byte{privateKeyOf(t, clear, members[0]), privateKeyOf(t, clear, members[1])}
	leftover := filepath.Join(dir, "network", ".wg-1234")
	if err := os.WriteFile(leftover, []byte("[Interface]\nPrivateKey = "+base64.StdEncoding.EncodeToString(keys[0])+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network", ".network-5678"), []byte(base64.StdEncoding.EncodeToString(keys[1])), 0600); err != nil {
		t.Fatal(err)
	}

	// A store that fails changes nothing.
	if err := (Host{Dir: dir, Keys: unsealable{}}).SealKeys(ctx); err == nil {
		t.Fatal("sealing passed with a store that can't seal")
	}
	for _, id := range members {
		if _, err := clear.Identity(ctx, id); err != nil {
			t.Fatal("a failed seal lost a key held in the clear", err)
		}
	}

	sealed := Host{Dir: dir, Keys: newMachine(t)}
	if err := sealed.SealKeys(ctx); err != nil {
		t.Fatal(err)
	}
	for i, id := range members {
		if identity, err := sealed.Identity(ctx, id); err != nil || identity != identities[id] {
			t.Fatal("sealing changed the host's identity on its network", err)
		}
		if holdsKey(t, dir, keys[i]) {
			t.Fatal("a private-link key is still in the clear after sealing")
		}
		if _, err := clear.Identity(ctx, id); !errors.Is(err, ErrSealedKey) {
			t.Fatal("a sealed key opened without the key store", err)
		}
	}
	if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an interrupted apply's configuration survived sealing")
	}
	// Sealing again rewrites nothing that is sealed already.
	before, _ := os.ReadFile(filepath.Join(dir, "network", members[0].String()+".key"))
	if err := sealed.SealKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "network", members[0].String()+".key")); !bytes.Equal(before, after) {
		t.Fatal("sealing again rewrote a sealed key")
	}
}

func TestSealKeysLeavesAHostWithoutNetworkingAlone(t *testing.T) {
	dir := t.TempDir()
	if err := (Host{Dir: dir, Keys: newMachine(t)}).SealKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "network")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sealing made network state on a host without any")
	}
}

func TestANewKeyIsNeverLeftInTheClearBesideAStore(t *testing.T) {
	ctx := context.Background()
	h := Host{Dir: t.TempDir(), Keys: unsealable{}}
	member := uuid.New()
	if _, err := h.Identity(ctx, member); err == nil {
		t.Fatal("an identity was made with a store that can't seal its key")
	}
	if _, err := os.Lstat(filepath.Join(h.Dir, "network", member.String()+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a key the store couldn't seal was kept")
	}
}

func TestApplyNeverMakesAKey(t *testing.T) {
	h := Host{Dir: t.TempDir(), Keys: newMachine(t)}
	member := uuid.New()
	if _, err := h.privateKey(context.Background(), member, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a missing key was made or failed otherwise", err)
	}
	if _, err := os.Lstat(filepath.Join(h.Dir, "network", member.String()+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading a missing key made one")
	}
}
