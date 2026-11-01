package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const localStateVersion = 1

type localStateKeys struct {
	Version int             `json:"agentStateVersion"`
	Keyring json.RawMessage `json:"keyring"`
}

// The agent-local envelope deliberately cannot be parsed as a cryptobox keyring.
// Previous agents reject it before initializing buckets, pruning or replaying.
func (j *Journal) openLocalKeys(point func(string) error) error {
	path := filepath.Join(j.dir, "keyring.json")
	info, err := os.Lstat(path)
	var raw []byte
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
		keys, err := cryptobox.New("host")
		if err != nil {
			return err
		}
		j.keys = keys
		raw, err = json.Marshal(keys)
		if err != nil {
			return err
		}
	} else {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 131072 {
			return errors.New("agent keyring must be an existing private regular file")
		}
		raw, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		var envelope localStateKeys
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&envelope) == nil && decoder.Decode(new(any)) == io.EOF {
			if envelope.Version != localStateVersion {
				return errors.New("unsupported agent local state version; use a compatible agent")
			}
			j.keys, err = cryptobox.Parse(envelope.Keyring)
			if err != nil {
				return err
			}
			return syncLocalStateDirectory(j.dir)
		}
		j.keys, err = cryptobox.Parse(raw)
		if err != nil {
			return errors.New("invalid or unsupported agent local state envelope")
		}
		if err = j.legacyMigrationAllowed(); err != nil {
			return err
		}
	}
	body, err := json.Marshal(localStateKeys{Version: localStateVersion, Keyring: raw})
	if err != nil {
		return err
	}
	return writeLocalState(j.dir, path, body, point)
}

// Migration keeps the update-admission boundary and does not change any command,
// checkpoint, report, ciphertext or key. Pending updates reconcile their original
// signed artifact after restart, including a replacement before success recording.
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
				if len(s.Writers) > 0 || hasUncertainRun(s.Runs) {
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
