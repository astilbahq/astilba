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

// sealedKey is a sealed key file.
type sealedKey struct {
	Version   int    `json:"agentStateVersion"`
	SealedKey []byte `json:"sealedKey"`
}

// ErrSealedKey says a private-link key is sealed to this machine and can't be opened now.
// Nothing changed.
var ErrSealedKey = errors.New("this server's private-link key is sealed to this machine, and it can't be opened now")

// keyName is the name membership id's key is sealed under. Binding the membership means a sealed
// key copied over another membership's file won't open.
func keyName(id uuid.UUID) string { return "astilba-agent-network-key-" + id.String() }

// The temporary files that network state writes rename into place. An earlier agent's interrupted
// apply could leave a WireGuard configuration holding the private key in the clear.
var networkLeftovers = []string{".network-", ".wg-"}

// privateKey reads membership id's private key, sealed or in the clear. With create, a missing
// key is generated. It is sealed when the host has a key store, or else held in the clear.
func (h Host) privateKey(ctx context.Context, id uuid.UUID, create bool) (*ecdh.PrivateKey, error) {
	if id == uuid.Nil {
		return nil, errors.New("membership identity required")
	}
	raw, err := privateRead(h.identityPath(id))
	if errors.Is(err, os.ErrNotExist) && create {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err = h.writeKey(ctx, id, key); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key, _, err := h.openKey(ctx, id, raw)
	return key, err
}

// openKey opens a key file's contents, and reports whether they were sealed.
func (h Host) openKey(ctx context.Context, id uuid.UUID, raw []byte) (*ecdh.PrivateKey, bool, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		var envelope sealedKey
		if json.Unmarshal([]byte(text), &envelope, json.RejectUnknownMembers(true)) != nil || envelope.Version != SealedKeyVersion || len(envelope.SealedKey) == 0 {
			return nil, true, errors.New("unsupported local network key format; use a compatible agent")
		}
		if h.Keys == nil {
			return nil, true, fmt.Errorf("%w: only the agent's own commands can open it", ErrSealedKey)
		}
		plain, err := h.Keys.Unseal(ctx, keyName(id), envelope.SealedKey)
		if err != nil {
			return nil, true, fmt.Errorf("%w: the key store can't read it (%v). It was sealed on another machine, or this host's TPM or systemd host key has changed. If that key is gone for good, remove this server from the network and add it again, so that it makes a new key", ErrSealedKey, err)
		}
		key, err := ecdh.X25519().NewPrivateKey(plain)
		if err != nil {
			return nil, true, errors.New("invalid local network key")
		}
		return key, true, nil
	}
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, false, errors.New("invalid local network key")
	}
	key, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, false, errors.New("invalid local network key")
	}
	return key, false, nil
}

// writeKey replaces membership id's key file with key. It seals the key with the host's key store
// when it has one, and reads the sealed key back before it replaces anything. Without a store it
// writes the key in the clear.
func (h Host) writeKey(ctx context.Context, id uuid.UUID, key *ecdh.PrivateKey) error {
	body := []byte(base64.StdEncoding.EncodeToString(key.Bytes()))
	if h.Keys != nil {
		sealed, err := h.Keys.Seal(ctx, keyName(id), key.Bytes())
		if err != nil {
			return err
		}
		// A key nothing can read would cut this host off its private network, so it replaces the
		// file only once read back.
		if back, err := h.Keys.Unseal(ctx, keyName(id), sealed); err != nil || !bytes.Equal(back, key.Bytes()) {
			return errors.New("the key store could not read back the private-link key it sealed")
		}
		if body, err = stablejson.Marshal(sealedKey{Version: SealedKeyVersion, SealedKey: sealed}); err != nil {
			return err
		}
	}
	return atomicPrivate(h.identityPath(id), body)
}

// SealKeys seals every private-link key this host holds in the clear with its key store. Each
// sealed key is read back before it replaces the key in the clear. SealKeys also removes the
// temporary files that interrupted writes left behind. A host that has never held managed network
// state has nothing to seal.
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
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".key") {
			continue
		}
		id, err := uuid.Parse(strings.TrimSuffix(entry.Name(), ".key"))
		if err != nil {
			return errors.New("invalid network key filename")
		}
		raw, err := privateRead(h.identityPath(id))
		if err != nil {
			return err
		}
		key, sealed, err := h.openKey(ctx, id, raw)
		if err != nil {
			return err
		}
		if !sealed {
			if err = h.writeKey(ctx, id, key); err != nil {
				return err
			}
		}
	}
	return nil
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
