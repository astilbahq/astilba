package agent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const localStateVersion = 1

// keyedStateVersion marks a journal whose agent holds a key. Agents from before keys refuse it, so
// a restored earlier binary stops before acting rather than presenting a bearer the bind retired.
const keyedStateVersion = 2

// sealedStateVersion marks a journal whose keyring is sealed to this machine's key store. Agents
// from before sealing refuse it before they act, and every agent that reads it holds keys.
const sealedStateVersion = 3

// StateVersion is the newest journal format this agent reads. describe reports it, and an agent
// installs only a release that reads the format its journal is in.
const StateVersion = sealedStateVersion

// ErrSealedJournal says the journal's keyring is sealed to this machine and can't be opened now:
// the key store is unavailable, or can't read what was sealed. Nothing in the journal changed.
var ErrSealedJournal = errors.New("the agent journal's keyring is sealed to this machine, and it can't be opened now")

// localStateKeys is keyring.json. Versions 1 and 2 hold the keyring in the clear; version 3 holds
// it sealed by the key store.
type localStateKeys struct {
	Version       int            `json:"agentStateVersion"`
	Keyring       jsontext.Value `json:"keyring,omitempty"`
	SealedKeyring []byte         `json:"sealedKeyring,omitempty"`
}

// The agent-local envelope deliberately cannot be parsed as a cryptobox keyring.
// Previous agents reject it before initializing buckets, pruning or replaying. With open set, a
// keyring in the clear is sealed to the store it returns, if that works now; one already sealed
// needs that store.
func (j *Journal) openLocalKeys(ctx context.Context, open func(context.Context) (KeyStore, error), point func(string) error) error {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	path := filepath.Join(j.dir, "keyring.json")
	info, err := os.Lstat(path)
	var keys *cryptobox.Keyring
	version, write := localStateVersion, true
	if errors.Is(err, os.ErrNotExist) {
		// An interrupted first open may leave only bbolt's empty file. Once
		// any bucket exists, missing keys must never be replaced.
		empty := false
		if err = j.db.View(func(tx *bolt.Tx) error { key, _ := tx.Cursor().First(); empty = key == nil; return nil }); err != nil {
			return err
		}
		if !empty {
			return errors.New("initialized agent journal is missing its keyring")
		}
		if keys, err = cryptobox.New("host"); err != nil {
			return err
		}
	} else {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 131072 {
			return errors.New("agent keyring must be an existing private regular file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var envelope localStateKeys
		if json.Unmarshal(raw, &envelope, json.RejectUnknownMembers(true)) == nil {
			switch {
			case (envelope.Version == localStateVersion || envelope.Version == keyedStateVersion) && len(envelope.Keyring) > 0 && envelope.SealedKeyring == nil:
				if keys, err = cryptobox.Parse(envelope.Keyring); err != nil {
					return err
				}
				version, write = envelope.Version, false
			case envelope.Version == sealedStateVersion && len(envelope.SealedKeyring) > 0 && len(envelope.Keyring) == 0:
				return j.openSealedKeys(ctx, open, envelope.SealedKeyring)
			default:
				return errors.New("unsupported agent local state version; use a compatible agent")
			}
		} else {
			if keys, err = cryptobox.Parse(raw); err != nil {
				return errors.New("invalid or unsupported agent local state envelope")
			}
			j.keys.Store(keys)
			if err = j.legacyMigrationAllowed(); err != nil {
				return err
			}
		}
	}
	j.keys.Store(keys)
	j.formatVersion.Store(int64(version))
	if open != nil {
		j.sealing = true
		store, err := open(ctx)
		if err == nil {
			// Sealing keeps the keys: the agent service replaces them when it starts.
			if err = j.sealWith(ctx, store, keys, point); err == nil {
				return nil
			}
		}
		slog.Warn("the agent journal's keyring stays unsealed until the key store can seal it", "error", err)
	}
	if !write {
		return syncLocalStateDirectory(j.dir)
	}
	return j.writeKeys(ctx, keys, version, point)
}

// openSealedKeys unseals keyring.json with this machine's key store.
func (j *Journal) openSealedKeys(ctx context.Context, open func(context.Context) (KeyStore, error), sealed []byte) error {
	if open == nil {
		return fmt.Errorf("%w: only the agent's own commands can open it", ErrSealedJournal)
	}
	store, keys, err := unsealKeys(ctx, open, sealed)
	if err != nil {
		return err
	}
	j.keys.Store(keys)
	j.formatVersion.Store(sealedStateVersion)
	j.sealing, j.store = true, store
	return syncLocalStateDirectory(j.dir)
}

// unsealKeys opens a sealed keyring with the store open returns.
func unsealKeys(ctx context.Context, open func(context.Context) (KeyStore, error), sealed []byte) (KeyStore, *cryptobox.Keyring, error) {
	store, err := open(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the key store is unavailable (%v). The agent service waits for it and tries again every minute", ErrSealedJournal, err)
	}
	plain, err := store.Unseal(ctx, journalCredentialName, sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the key store can't read it (%v). It was sealed on another machine, or this host's TPM or systemd host key has changed. If that key is gone for good, the journal can't be opened again: revoke the server and enroll this host as a new server with a fresh state directory", ErrSealedJournal, err)
	}
	keys, err := cryptobox.Parse(plain)
	if err != nil {
		return nil, nil, err
	}
	return store, keys, nil
}

// SealedJournalNote says why the journal in dir can't be opened now, for preflight, or returns ""
// when nothing seals it shut. It opens the keyring only to check, and keeps nothing.
func SealedJournalNote(ctx context.Context, dir string, open func(context.Context) (KeyStore, error)) string {
	raw, err := os.ReadFile(filepath.Join(dir, "keyring.json"))
	if err != nil {
		return ""
	}
	var envelope localStateKeys
	if json.Unmarshal(raw, &envelope, json.RejectUnknownMembers(true)) != nil || envelope.Version != sealedStateVersion || len(envelope.SealedKeyring) == 0 {
		return ""
	}
	if _, _, err = unsealKeys(ctx, open, envelope.SealedKeyring); err != nil {
		return err.Error()
	}
	return ""
}

// sealWith seals keyring.json, holding keys, to store from now on. The caller holds stateMu.
func (j *Journal) sealWith(ctx context.Context, store KeyStore, keys *cryptobox.Keyring, point func(string) error) error {
	j.store = store
	if err := j.writeKeys(ctx, keys, sealedStateVersion, point); err != nil {
		j.store = nil
		return err
	}
	return nil
}

// writeKeys replaces keyring.json with keys: sealed by the journal's key store when it has one, or
// else in the clear at version. The caller holds stateMu.
func (j *Journal) writeKeys(ctx context.Context, keys *cryptobox.Keyring, version int, point func(string) error) error {
	raw, err := stablejson.Marshal(keys)
	if err != nil {
		return err
	}
	envelope := localStateKeys{Version: version, Keyring: raw}
	if j.store != nil {
		sealed, err := j.store.Seal(ctx, journalCredentialName, raw)
		if err != nil {
			return err
		}
		// Keys nothing can read would lose the journal, so they replace the file only once read back.
		if back, err := j.store.Unseal(ctx, journalCredentialName, sealed); err != nil || !bytes.Equal(back, raw) {
			return errors.New("the key store could not read back the journal keyring it sealed")
		}
		envelope = localStateKeys{Version: sealedStateVersion, SealedKeyring: sealed}
	}
	body, err := stablejson.Marshal(envelope)
	if err != nil {
		return err
	}
	if err = writeLocalState(j.dir, filepath.Join(j.dir, "keyring.json"), body, point); err != nil {
		return err
	}
	j.formatVersion.Store(int64(envelope.Version))
	return nil
}

// Migration keeps the update-admission boundary and does not change any command,
// checkpoint, report, ciphertext or key. Pending updates reconcile their original
// signed artifact after restart, including a replacement before success recording.
// The open that migrates then redacts finished records, as every open does.
func (j *Journal) legacyMigrationAllowed() error {
	return j.db.View(func(tx *bolt.Tx) error {
		refuse := errors.New("legacy agent state has unfinished host work; finish it with the previous agent before upgrading")
		s, err := j.crossHostIn(tx)
		if err != nil {
			return err
		}
		if s.ParentID != uuid.Nil {
			return refuse
		}
		var authority recoveredAuthority
		if err = j.get(tx, "recovery-authority", "current", &authority); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if authority.Handover.Authority.ParentID != uuid.Nil && !authority.Open {
			return refuse
		}
		for _, bucket := range []string{"upgrades", "operations"} {
			b := tx.Bucket([]byte(bucket))
			if b == nil {
				continue
			}
			if err = b.ForEach(func(k, _ []byte) error {
				if bucket == "upgrades" {
					var u upgradeState
					if err := j.get(tx, bucket, string(k), &u); err != nil {
						return err
					}
					if u.Status.Active() {
						return refuse
					}
				} else {
					if j.isCrossHostSentinel(tx, string(k)) || j.isDistributedFence(tx, string(k)) || j.isRecoverySentinel(tx, string(k)) {
						return nil
					}
					var r Record
					if err := j.get(tx, bucket, string(k), &r); err != nil {
						return err
					}
					if !r.Acknowledged && r.Command.Kind != "update" {
						return refuse
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, bucket := range []string{"schedules-v2", "schedules"} {
			b := tx.Bucket([]byte(bucket))
			if b == nil {
				continue
			}
			if err = b.ForEach(func(k, _ []byte) error {
				id := string(k)
				if seen[id] {
					return nil
				}
				seen[id] = true
				s, err := j.scheduleIn(tx, id)
				if err != nil {
					return err
				}
				if len(s.Writers) > 0 || hasUncertainRun(s) {
					return refuse
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeLocalState(dir, path string, body []byte, point func(string) error) error {
	at := func(name string) error {
		if point != nil {
			return point(name)
		}
		return nil
	}
	f, err := os.CreateTemp(dir, ".agent-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = at("local-state-written"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = at("local-state-synced"); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if err = at("local-state-renamed"); err != nil {
		return err
	}
	if err = syncLocalStateDirectory(dir); err != nil {
		return err
	}
	return at("local-state-directory-synced")
}

func syncLocalStateDirectory(dir string) error {
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// markKeyed raises the journal's format version once its agent holds a key. A sealed journal is
// keyed already: only agents with keys read it.
func (j *Journal) markKeyed() error {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	if j.formatVersion.Load() >= keyedStateVersion {
		return nil
	}
	return j.writeKeys(context.Background(), j.keys.Load(), keyedStateVersion, nil)
}

// HoldsKey reports whether this agent authenticates with a key, or once did: either way, an agent
// from before keys can't run on this journal.
func (j *Journal) HoldsKey(cfg Config) bool {
	return cfg.Key != nil || cfg.BindKey != nil || cfg.PendingRotation != nil || j.formatVersion.Load() >= keyedStateVersion
}

// StateVersion is the format the journal is in.
func (j *Journal) StateVersion() int { return int(j.formatVersion.Load()) }

// KeyStore is the store the journal's keyring is sealed to, or nil while it isn't sealed.
func (j *Journal) KeyStore() KeyStore {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	return j.store
}
