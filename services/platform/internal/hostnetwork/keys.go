package hostnetwork

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Sealer seals values to this machine under a name. It refuses to unseal a value sealed on
// another machine or under another name, so one sealed value can't stand in for another. The
// agent's key store is one.
type Sealer interface {
	Seal(ctx context.Context, name string, plain []byte) ([]byte, error)
	Unseal(ctx context.Context, name string, sealed []byte) ([]byte, error)
}

// SealedKeyVersion is the format of a private-link key sealed to this machine. It is the agent
// state version that introduced it. A key file used to hold the key in the clear, as base64. An
// agent from before sealing reads a sealed one as an invalid key and refuses it. Because the
// file is still there, it doesn't make a new key in its place.
const SealedKeyVersion = 4

// sealedKey is a sealed key file. Its public key is in the clear, since it isn't secret, so the
// host's identity on the network never needs the key store. Applying the network opens the
// private key and checks it against the public one.
type sealedKey struct {
	Version   int    `json:"agentStateVersion"`
	PublicKey string `json:"publicKey"`
	SealedKey []byte `json:"sealedKey"`
}

// ErrSealedKey says this machine's key store works but can't open a private-link key: it was sealed
// on another machine, or this host's TPM or systemd host key has changed. Nothing changed, and no
// new key was made in its place.
var ErrSealedKey = errors.New("this server's private-link key is sealed, and this machine's key store can't open it")

// ErrKeyStoreUnavailable says the key store that seals private-link keys isn't working now, as
// early in boot. Nothing changed; trying again later may work.
var ErrKeyStoreUnavailable = errors.New("the key store that seals this server's private-link keys isn't working now")

// keyName is the name membership id's key is sealed under. Binding the membership means a sealed
// key copied over another membership's file won't open.
func keyName(id uuid.UUID) string { return "astilba-agent-network-key-" + id.String() }

// probeName is the name of the value sealed to check whether the key store works.
const probeName = "astilba-agent-network-key-probe"

// The temporary files that network state writes rename into place. An earlier agent's interrupted
// apply could leave a WireGuard configuration holding the private key in the clear.
var networkLeftovers = []string{".network-", ".wg-"}

// keyFile is a key file as read, without opening a sealed key. It holds either the private key in
// the clear or the sealed one, and the public key either way.
type keyFile struct {
	public string
	clear  *ecdh.PrivateKey
	sealed []byte
}

// readKeyFile reads membership id's key file without opening a sealed key.
func (h Host) readKeyFile(id uuid.UUID) (keyFile, error) {
	if id == uuid.Nil {
		return keyFile{}, errors.New("membership identity required")
	}
	raw, err := privateRead(h.identityPath(id))
	if err != nil {
		return keyFile{}, err
	}
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		var envelope sealedKey
		if json.Unmarshal([]byte(text), &envelope, json.RejectUnknownMembers(true)) != nil || envelope.Version != SealedKeyVersion || !PublicKey(envelope.PublicKey) || len(envelope.SealedKey) == 0 {
			return keyFile{}, errors.New("unsupported local network key format; use a compatible agent")
		}
		return keyFile{public: envelope.PublicKey, sealed: envelope.SealedKey}, nil
	}
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return keyFile{}, errors.New("invalid local network key")
	}
	key, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return keyFile{}, errors.New("invalid local network key")
	}
	return keyFile{public: publicKeyOf(key), clear: key}, nil
}

func publicKeyOf(key *ecdh.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
}

// publicKey is membership id's public key. A membership without a key gets a new one, sealed when
// the host has a key store, or else held in the clear. It never opens a sealed key.
func (h Host) publicKey(ctx context.Context, id uuid.UUID) (string, error) {
	f, err := h.readKeyFile(id)
	if errors.Is(err, os.ErrNotExist) {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		if err = h.writeKey(ctx, id, key); err != nil {
			return "", err
		}
		return publicKeyOf(key), nil
	}
	return f.public, err
}

// privateKey opens membership id's private key. It never makes one: a missing key is an error.
func (h Host) privateKey(ctx context.Context, id uuid.UUID) (*ecdh.PrivateKey, error) {
	f, err := h.readKeyFile(id)
	if err != nil {
		return nil, err
	}
	return h.openKeyFile(ctx, id, f)
}

// openKeyFile opens the private key in membership id's key file f.
func (h Host) openKeyFile(ctx context.Context, id uuid.UUID, f keyFile) (*ecdh.PrivateKey, error) {
	if f.clear != nil {
		return f.clear, nil
	}
	if h.Keys == nil {
		return nil, fmt.Errorf("%w: this command has no key store to open it with", ErrSealedKey)
	}
	plain, err := h.Keys.Unseal(ctx, keyName(id), f.sealed)
	if err != nil {
		return nil, h.unsealFailure(ctx, err)
	}
	key, err := ecdh.X25519().NewPrivateKey(plain)
	if err != nil || publicKeyOf(key) != f.public {
		return nil, fmt.Errorf("%w: it doesn't match the public key beside it", ErrSealedKey)
	}
	return key, nil
}

// unsealFailure says why a sealed key didn't open. A store that says it isn't working is believed.
// Otherwise the store is checked with a value of its own: if that works, the store is fine and
// this key isn't one it can open.
func (h Host) unsealFailure(ctx context.Context, cause error) error {
	if errors.Is(cause, ErrKeyStoreUnavailable) {
		return cause
	}
	probe := []byte("astilba private-link key store probe")
	if sealed, err := h.Keys.Seal(ctx, probeName, probe); err == nil {
		if back, err := h.Keys.Unseal(ctx, probeName, sealed); err == nil && bytes.Equal(back, probe) {
			return fmt.Errorf("%w (%v). It was sealed on another machine, or this host's TPM or systemd host key has changed. If that key is gone for good, remove this server from the network and add it again, so that it gets a new key", ErrSealedKey, cause)
		}
	}
	return fmt.Errorf("%w: %w", ErrKeyStoreUnavailable, cause)
}

// writeKey replaces membership id's key file with key: sealed with the host's key store when it has
// one, or else in the clear.
func (h Host) writeKey(ctx context.Context, id uuid.UUID, key *ecdh.PrivateKey) error {
	body := []byte(base64.StdEncoding.EncodeToString(key.Bytes()))
	if h.Keys != nil {
		var err error
		if body, err = h.sealKey(ctx, id, key); err != nil {
			return err
		}
	}
	return atomicPrivate(h.identityPath(id), body)
}

// sealKey is the key file that holds key sealed with the host's key store. The sealed key is read
// back first: a key nothing can read would cut this host off its private network.
func (h Host) sealKey(ctx context.Context, id uuid.UUID, key *ecdh.PrivateKey) ([]byte, error) {
	sealed, err := h.Keys.Seal(ctx, keyName(id), key.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyStoreUnavailable, err)
	}
	if back, err := h.Keys.Unseal(ctx, keyName(id), sealed); err != nil || !bytes.Equal(back, key.Bytes()) {
		return nil, fmt.Errorf("%w: it could not read back the private-link key it sealed", ErrKeyStoreUnavailable)
	}
	return stablejson.Marshal(sealedKey{Version: SealedKeyVersion, PublicKey: publicKeyOf(key), SealedKey: sealed})
}

// Prepared is a key store that does its work ahead of time, before any lock is taken, so that work
// done later under a lock replays it rather than call the store. Once Replay is called, it answers
// only what it did before, and refuses anything else as not working.
type Prepared struct {
	store  Sealer
	mu     sync.Mutex
	replay bool
	sealed map[string][]byte
	opened map[string][]byte
}

// errNotPrepared says a key store call wasn't made ahead of time, so it isn't made now.
var errNotPrepared = fmt.Errorf("%w: it wasn't used ahead of time", ErrKeyStoreUnavailable)

// Prepare is a Prepared store that does its work with store.
func Prepare(store Sealer) *Prepared {
	return &Prepared{store: store, sealed: map[string][]byte{}, opened: map[string][]byte{}}
}

// Replay makes p answer only what it did before.
func (p *Prepared) Replay() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replay = true
}

func (p *Prepared) Seal(ctx context.Context, name string, plain []byte) ([]byte, error) {
	at := name + "\x00" + string(plain)
	p.mu.Lock()
	if v, ok := p.sealed[at]; ok || p.replay {
		p.mu.Unlock()
		if !ok {
			return nil, errNotPrepared
		}
		return v, nil
	}
	p.mu.Unlock()
	sealed, err := p.store.Seal(ctx, name, plain)
	if err == nil {
		p.mu.Lock()
		p.sealed[at] = sealed
		p.mu.Unlock()
	}
	return sealed, err
}

func (p *Prepared) Unseal(ctx context.Context, name string, sealed []byte) ([]byte, error) {
	at := name + "\x00" + string(sealed)
	p.mu.Lock()
	if v, ok := p.opened[at]; ok || p.replay {
		p.mu.Unlock()
		if !ok {
			return nil, errNotPrepared
		}
		return v, nil
	}
	p.mu.Unlock()
	plain, err := p.store.Unseal(ctx, name, sealed)
	if err == nil {
		p.mu.Lock()
		p.opened[at] = plain
		p.mu.Unlock()
	}
	return plain, err
}

// PrepareSealKeys seals, with the host's key store, the keys SealKeys would seal, without taking
// the host lock or writing anything, so that SealKeys can then replay it from a Prepared store.
func (h Host) PrepareSealKeys(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "network"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		id, err := uuid.Parse(strings.TrimSuffix(entry.Name(), ".key"))
		if !strings.HasSuffix(entry.Name(), ".key") || err != nil || id.String()+".key" != entry.Name() {
			continue
		}
		if f, err := h.readKeyFile(id); err == nil && f.clear != nil {
			if _, err = h.sealKey(ctx, id, f.clear); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// liveKey is the private key m's interface holds, when it is the key m names, or else nil.
func (h Host) liveKey(ctx context.Context, m Manifest) *ecdh.PrivateKey {
	out, err := h.run(ctx, "wg", nil, "show", m.Interface(), "private-key")
	if err != nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil || publicKeyOf(key) != m.PublicKey {
		return nil
	}
	return key
}

// SealKeys seals every private-link key this host holds in the clear with its key store. Each
// sealed key is read back before it replaces the key in the clear. A key file it can't handle
// doesn't stop it sealing the others; it reports every one. It never opens a key sealed already.
// SealKeys also removes the temporary files that interrupted writes left behind. A host that has
// never held managed network state has nothing to seal.
func (h Host) SealKeys(ctx context.Context) error {
	if h.Keys == nil {
		return errors.New("private-link keys need a key store to be sealed")
	}
	dir := filepath.Join(h.Dir, "network")
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err = removeNetworkLeftovers(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".key") {
			continue
		}
		id, err := uuid.Parse(strings.TrimSuffix(entry.Name(), ".key"))
		if err != nil || id.String()+".key" != entry.Name() {
			failures = append(failures, fmt.Errorf("%s isn't a private-link key file this agent wrote", entry.Name()))
			continue
		}
		f, err := h.readKeyFile(id)
		if err == nil && f.clear != nil {
			err = h.writeKey(ctx, id, f.clear)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

// removeNetworkLeftovers removes the temporary files that interrupted network state writes left
// behind. The caller holds the host lock, so none of them is still being written.
func removeNetworkLeftovers(dir string) error {
	removed := false
	for _, prefix := range networkLeftovers {
		leftovers, err := filepath.Glob(filepath.Join(dir, prefix+"*"))
		if err != nil {
			return err
		}
		for _, leftover := range leftovers {
			if err = os.Remove(leftover); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			removed = true
		}
	}
	if !removed {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
