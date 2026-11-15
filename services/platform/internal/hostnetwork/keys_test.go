package hostnetwork

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	key, err := h.privateKey(context.Background(), id)
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

// counting is a machine that counts the calls made to it.
type counting struct {
	machine
	calls int
}

func (c *counting) Seal(ctx context.Context, name string, plain []byte) ([]byte, error) {
	c.calls++
	return c.machine.Seal(ctx, name, plain)
}

func (c *counting) Unseal(ctx context.Context, name string, sealed []byte) ([]byte, error) {
	c.calls++
	return c.machine.Unseal(ctx, name, sealed)
}

func TestCopiedStateYieldsNoPrivateLinkKeyOnAnotherMachine(t *testing.T) {
	ctx := context.Background()
	here := Host{Dir: t.TempDir(), Keys: newMachine(t)}
	member := uuid.New()
	identity, err := here.Identity(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	key := privateKeyOf(t, here, member)
	if publicKeyOf(mustKey(t, key)) != identity.PublicKey {
		t.Fatal("the sealed key doesn't open on its own machine as the key the host reported")
	}
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
	for name, tc := range map[string]struct {
		keys Sealer
		want error
	}{
		"another machine": {newMachine(t), ErrSealedKey},
		"no key store":    {nil, ErrSealedKey},
		// A store that doesn't work says so, rather than blaming the key.
		"a store that isn't working": {unsealable{}, ErrKeyStoreUnavailable},
	} {
		elsewhere := Host{Dir: copied, Keys: tc.keys}
		if _, err := elsewhere.privateKey(ctx, member); !errors.Is(err, tc.want) {
			t.Fatalf("%s: the copied key opened or failed otherwise: %v", name, err)
		}
		// The host's identity needs no secret, so it stays as it was.
		if got, err := elsewhere.Identity(ctx, member); err != nil || got != identity {
			t.Fatalf("%s: the copied key's identity changed: %v", name, err)
		}
		if tc.keys != nil {
			if err := elsewhere.SealKeys(ctx); err != nil {
				t.Fatalf("%s: sealing failed over a key sealed already: %v", name, err)
			}
		}
		// Nothing replaced the key that can't be read, which would give the server a new identity.
		if raw, _ := os.ReadFile(filepath.Join(copied, "network", member.String()+".key")); !bytes.Equal(raw, sealed) {
			t.Fatalf("%s: the copied key file changed", name)
		}
	}
}

func mustKey(t *testing.T, raw []byte) *ecdh.PrivateKey {
	t.Helper()
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
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
	if _, err = h.privateKey(ctx, second); !errors.Is(err, ErrSealedKey) {
		t.Fatal("one membership's sealed key opened as another's", err)
	}
}

// A sealed key whose public key beside it was changed doesn't open as that public key's.
func TestSealedKeyMustMatchItsPublicKey(t *testing.T) {
	ctx := context.Background()
	h := Host{Dir: t.TempDir(), Keys: newMachine(t)}
	member := uuid.New()
	if _, err := h.Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.Dir, "network", member.String()+".key")
	var envelope sealedKey
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	envelope.PublicKey = publicKeyOf(other)
	raw, _ = stablejson.Marshal(envelope)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.privateKey(ctx, member); !errors.Is(err, ErrSealedKey) {
		t.Fatal("a sealed key opened under another public key", err)
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
	if err := (Host{Dir: dir, Keys: unsealable{}}).SealKeys(ctx); !errors.Is(err, ErrKeyStoreUnavailable) {
		t.Fatal("sealing passed, or failed otherwise, with a store that can't seal", err)
	}
	for i, id := range members {
		if !bytes.Equal(privateKeyOf(t, clear, id), keys[i]) {
			t.Fatal("a failed seal lost a key held in the clear")
		}
	}

	store := &counting{machine: newMachine(t)}
	sealed := Host{Dir: dir, Keys: store}
	if err := sealed.SealKeys(ctx); err != nil {
		t.Fatal(err)
	}
	for i, id := range members {
		if identity, err := sealed.Identity(ctx, id); err != nil || identity != identities[id] {
			t.Fatal("sealing changed the host's identity on its network", err)
		}
		if !bytes.Equal(privateKeyOf(t, sealed, id), keys[i]) {
			t.Fatal("sealing changed the private-link key")
		}
		if holdsKey(t, dir, keys[i]) {
			t.Fatal("a private-link key is still in the clear after sealing")
		}
		if _, err := clear.privateKey(ctx, id); !errors.Is(err, ErrSealedKey) {
			t.Fatal("a sealed key opened without the key store", err)
		}
	}
	if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an interrupted apply's configuration survived sealing")
	}
	// Sealing again rewrites nothing that is sealed already, and doesn't open it to find out.
	before, _ := os.ReadFile(filepath.Join(dir, "network", members[0].String()+".key"))
	store.calls = 0
	if err := sealed.SealKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "network", members[0].String()+".key")); !bytes.Equal(before, after) || store.calls != 0 {
		t.Fatal("sealing again rewrote or opened a sealed key")
	}
}

// A key file sealing can't handle doesn't keep it from sealing the keys after it.
func TestSealKeysSealsEveryKeyItCan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	clear := Host{Dir: dir}
	member := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	if _, err := clear.Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	key := privateKeyOf(t, clear, member)
	// Sorted before it: a name the agent never writes, and a key file others can read.
	upper := strings.ToUpper(uuid.New().String()) + ".key"
	if err := os.WriteFile(filepath.Join(dir, "network", upper), []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	open := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	if _, err := clear.Identity(ctx, open); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "network", open.String()+".key"), 0644); err != nil {
		t.Fatal(err)
	}
	err := (Host{Dir: dir, Keys: newMachine(t)}).SealKeys(ctx)
	if err == nil || !strings.Contains(err.Error(), upper) || !strings.Contains(err.Error(), open.String()) {
		t.Fatal("sealing didn't report every key file it couldn't handle", err)
	}
	if holdsKey(t, filepath.Join(dir, "network", member.String()+".key"), key) {
		t.Fatal("a key file sealing couldn't handle kept it from sealing the next one")
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
	if _, err := h.Identity(ctx, member); !errors.Is(err, ErrKeyStoreUnavailable) {
		t.Fatal("an identity was made with a store that can't seal its key, or failed otherwise", err)
	}
	if _, err := os.Lstat(filepath.Join(h.Dir, "network", member.String()+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a key the store couldn't seal was kept")
	}
}

// A key held in the clear opens without the key store, as on a host that has none.
func TestKeyInTheClearNeedsNoKeyStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	member := uuid.New()
	if _, err := (Host{Dir: dir}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	store := &counting{machine: newMachine(t)}
	if _, err := (Host{Dir: dir, Keys: store}).privateKey(ctx, member); err != nil || store.calls != 0 {
		t.Fatal("a key held in the clear needed the key store", err)
	}
}

// A Prepared store replays what it did before Replay, without calling the store, and refuses
// anything else as not working.
func TestPreparedStoreReplaysOnlyWhatItDid(t *testing.T) {
	ctx := context.Background()
	store := &counting{machine: newMachine(t)}
	dir := t.TempDir()
	member := uuid.New()
	if _, err := (Host{Dir: dir, Keys: store}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	prepared := Prepare(store)
	h := Host{Dir: dir, Keys: prepared}
	if _, err := h.privateKey(ctx, member); err != nil {
		t.Fatal(err)
	}
	prepared.Replay()
	store.calls = 0
	if _, err := h.privateKey(ctx, member); err != nil || store.calls != 0 {
		t.Fatal("a prepared key wasn't replayed without the store", err, store.calls)
	}
	other := uuid.New()
	if _, err := (Host{Dir: dir, Keys: store}).Identity(ctx, other); err != nil {
		t.Fatal(err)
	}
	store.calls = 0
	if _, err := h.privateKey(ctx, other); !errors.Is(err, ErrKeyStoreUnavailable) || store.calls != 0 {
		t.Fatal("a key that wasn't prepared was opened, or blamed on the key", err, store.calls)
	}
}

// failing is a key store that says it isn't working, and counts the calls made to it.
type failing struct{ calls int }

func (f *failing) Seal(context.Context, string, []byte) ([]byte, error) {
	f.calls++
	return nil, fmt.Errorf("%w: timed out", ErrKeyStoreUnavailable)
}

func (f *failing) Unseal(context.Context, string, []byte) ([]byte, error) {
	f.calls++
	return nil, fmt.Errorf("%w: timed out", ErrKeyStoreUnavailable)
}

// A store that says it isn't working is believed, rather than probed again: a probe that works a
// moment later would blame the key.
func TestAStoreThatIsNotWorkingIsNotProbed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	member := uuid.New()
	if _, err := (Host{Dir: dir, Keys: newMachine(t)}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	store := &failing{}
	if _, err := (Host{Dir: dir, Keys: store}).privateKey(ctx, member); !errors.Is(err, ErrKeyStoreUnavailable) || store.calls != 1 {
		t.Fatal("a store that isn't working was probed or the key blamed", err, store.calls)
	}
}

// A Prepared store replays a failure as it happened, so a key that isn't this machine's is blamed
// under the lock too, not the store.
func TestPreparedStoreReplaysFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	member := uuid.New()
	if _, err := (Host{Dir: dir, Keys: newMachine(t)}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	store := &counting{machine: newMachine(t)}
	prepared := Prepare(store)
	h := Host{Dir: dir, Keys: prepared}
	if _, err := h.privateKey(ctx, member); !errors.Is(err, ErrSealedKey) {
		t.Fatal(err)
	}
	prepared.Replay()
	store.calls = 0
	if _, err := h.privateKey(ctx, member); !errors.Is(err, ErrSealedKey) || store.calls != 0 {
		t.Fatal("a replayed failure wasn't the key's, or called the store", err, store.calls)
	}
}

// Restoring a link costs the key store only when its key is sealed: a key held in the clear, or a
// removal, needs none.
func TestOnlyASealedKeyNeedsTheKeyStoreToRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	clear, sealed := uuid.New(), uuid.New()
	if _, err := (Host{Dir: dir}).Identity(ctx, clear); err != nil {
		t.Fatal(err)
	}
	store := newMachine(t)
	if _, err := (Host{Dir: dir, Keys: store}).Identity(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	h := Host{Dir: dir, Keys: store, WG: "/nonexistent/wg"}
	if h.needsKey(ctx, Manifest{MembershipID: clear}) {
		t.Fatal("a key held in the clear needed the key store")
	}
	if !h.needsKey(ctx, Manifest{MembershipID: sealed}) {
		t.Fatal("a sealed key with no live link didn't need the key store")
	}
	if h.needsKey(ctx, Manifest{MembershipID: sealed, Disabled: true}) {
		t.Fatal("a removal needed the key store")
	}
}

// A restriction whose save fails keeps the mark of the restriction it would have replaced, so the
// restore still finishes that one as a restriction. Saving another intent clears both.
func TestARestrictionsMarkOutlivesAFailedSaveOfTheNext(t *testing.T) {
	network := uuid.New()
	first := Manifest{NetworkID: network, Generation: 1}
	second := Manifest{NetworkID: network, Generation: 2}
	h := Host{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(h.Dir, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.markRestriction(first, true, Manifest{}); err != nil || !h.restrictionMarked(first) {
		t.Fatal("a restriction wasn't marked", err)
	}
	// The second restriction marks itself, then its save fails: the first is still saved.
	if err := h.markRestriction(second, true, first); err != nil {
		t.Fatal(err)
	}
	if !h.restrictionMarked(first) || !h.restrictionMarked(second) {
		t.Fatal("marking the next restriction dropped the saved one's mark")
	}
	if err := h.markRestriction(Manifest{NetworkID: network, Generation: 3}, false, second); err != nil || h.restrictionMarked(first) || h.restrictionMarked(second) {
		t.Fatal("saving another intent didn't clear the marks", err)
	}
}
