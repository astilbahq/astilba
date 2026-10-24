package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type Config struct {
	Origin               string
	ControllerGeneration int64
	ServerID, MachineID  uuid.UUID
	Credential, Pending  string
	EnrollmentToken      string
	Enrolled             bool
	Role                 api.ServerRole
	PublicKey            []byte
}
type Record struct {
	Command      api.ExecutionCommand
	Report       api.AgentReport
	Checkpoint   string
	Acknowledged bool
}
type Journal struct {
	db   *bolt.DB
	keys *cryptobox.Keyring
	dir  string
}

func Open(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state directory must be private (0700) and not a symlink")
	}
	db, err := bolt.Open(filepath.Join(dir, "journal.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Journal, error) { db.Close(); return nil, err }
	path := filepath.Join(dir, "keyring.json")
	if _, err = os.Stat(path); os.IsNotExist(err) {
		keys, e := cryptobox.New("host")
		if e != nil {
			return fail(e)
		}
		if e = keys.WriteNew(path); e != nil {
			return fail(e)
		}
	}
	keys, err := cryptobox.Load(path)
	if err != nil {
		return fail(err)
	}
	keyFile, err := os.Open(path)
	if err != nil {
		return fail(err)
	}
	err = keyFile.Sync()
	keyFile.Close()
	if err != nil {
		return fail(err)
	}
	parent, err := os.Open(dir)
	if err != nil {
		return fail(err)
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return fail(err)
	}
	j := &Journal{db: db, keys: keys, dir: dir}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range []string{"identity", "operations", "generations", "units", "routing", "schedules", "upgrades"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(b)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	return j, nil
}
func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) get(tx *bolt.Tx, bucket, key string, out any) error {
	b := tx.Bucket([]byte(bucket)).Get([]byte(key))
	if b == nil {
		return os.ErrNotExist
	}
	plain, e := j.keys.Open(b, bucket+":"+key)
	if e != nil {
		return e
	}
	return json.Unmarshal(plain, out)
}
func (j *Journal) put(tx *bolt.Tx, bucket, key string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	limit := 32768
	if bucket == "units" || bucket == "routing" || bucket == "schedules" || bucket == "upgrades" {
		limit = 524288
	}
	if record, ok := value.(Record); ok && (record.Command.Kind == "visual" || record.Command.Kind == "pipeline" || record.Command.Kind == "backup" || record.Command.Kind == "upgrade") {
		limit = 524288
	}
	if len(raw) > limit {
		return errors.New("journal record limit exceeded")
	}
	data, e := j.keys.Seal(raw, bucket+":"+key)
	if e != nil {
		return e
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), data)
}
func (j *Journal) Config() (Config, error) {
	var c Config
	e := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "identity", "config", &c) })
	return c, e
}
func (j *Journal) SetConfig(c Config) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "identity", "config", c) })
}
func (j *Journal) Record(id uuid.UUID) (Record, error) {
	var r Record
	e := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "operations", id.String(), &r) })
	return r, e
}
func (j *Journal) Pending() (out []Record, err error) {
	err = j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("operations")).ForEach(func(k, v []byte) error {
			var r Record
			if e := j.get(tx, "operations", string(k), &r); e != nil {
				return e
			}
			if !r.Acknowledged {
				out = append(out, r)
			}
			return nil
		})
	})
	return
}
func sameCommand(a, b api.ExecutionCommand) bool {
	if a.Kind == "visual" && b.Kind == "visual" {
		var ap, bp deployment.Payload
		if a.Visual == nil || b.Visual == nil || json.Unmarshal(*a.Visual, &ap) != nil || json.Unmarshal(*b.Visual, &bp) != nil || ap.Digest() != a.PlanHash || bp.Digest() != b.PlanHash {
			return false
		}
		a.Visual, b.Visual = nil, nil
	}
	if a.Kind == "upgrade" && b.Kind == "upgrade" {
		var ap, bp upgrade.Command
		if a.Upgrade == nil || b.Upgrade == nil || json.Unmarshal(*a.Upgrade, &ap) != nil || json.Unmarshal(*b.Upgrade, &bp) != nil || ap.Digest() != a.PlanHash || bp.Digest() != b.PlanHash {
			return false
		}
		a.Upgrade, b.Upgrade = nil, nil
	}
	if a.Kind == "backup" && b.Kind == "backup" {
		var ap, bp backup.Command
		if a.Backup == nil || b.Backup == nil || json.Unmarshal(*a.Backup, &ap) != nil || json.Unmarshal(*b.Backup, &bp) != nil || ap.Digest() != a.PlanHash || bp.Digest() != b.PlanHash {
			return false
		}
		a.Backup, b.Backup = nil, nil
	}
	if a.Kind == "pipeline" && b.Kind == "pipeline" {
		var ap, bp pipeline.Command
		if a.Pipeline == nil || b.Pipeline == nil || json.Unmarshal(*a.Pipeline, &ap) != nil || json.Unmarshal(*b.Pipeline, &bp) != nil || ap.Digest() != a.PlanHash || bp.Digest() != b.PlanHash {
			return false
		}
		// Credentials are hydrated transport data; the plan hash freezes non-secret inputs.
		// Keep the original hydrated journal entry when a credential-free cancel arrives.
		a.Pipeline, b.Pipeline = nil, nil
	}
	a.CancelRequested = false
	b.CancelRequested = false
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return bytes.Equal(aj, bj)
}
func (j *Journal) Accept(c api.ExecutionCommand) (Record, error) {
	var out Record
	err := j.db.Update(func(tx *bolt.Tx) error {
		if e := j.checkController(tx, c); e != nil {
			return e
		}
		var existing Record
		if e := j.get(tx, "operations", c.Id.String(), &existing); e == nil {
			if !sameCommand(existing.Command, c) {
				return errors.New("command identity changed")
			}
			existing.Command.CancelRequested = existing.Command.CancelRequested || c.CancelRequested
			out = existing
			return j.put(tx, "operations", c.Id.String(), out)
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		count := 0
		var oldest string
		if e := tx.Bucket([]byte("operations")).ForEach(func(k, v []byte) error {
			var r Record
			if e := j.get(tx, "operations", string(k), &r); e != nil {
				return e
			}
			if !r.Acknowledged {
				return errors.New("a command still awaits completion or acknowledgement")
			}
			count++
			if oldest == "" {
				oldest = string(k)
			}
			return nil
		}); e != nil {
			return e
		}
		if count >= 128 {
			if e := tx.Bucket([]byte("operations")).Delete([]byte(oldest)); e != nil {
				return e
			}
		}
		var generation int64
		key := c.EnvironmentId.String()
		if c.Kind == "update" {
			key = "agent-update"
		}
		e := j.get(tx, "generations", key, &generation)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if c.Generation <= generation {
			return errors.New("stale generation")
		}
		if e := j.put(tx, "generations", key, c.Generation); e != nil {
			return e
		}
		out = Record{Command: c, Report: api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}, Checkpoint: "accepted"}
		return j.put(tx, "operations", c.Id.String(), out)
	})
	return out, err
}
func (j *Journal) Acknowledge(id uuid.UUID, sequence int64) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if e := j.get(tx, "operations", id.String(), &r); e != nil {
			return e
		}
		if r.Report.Sequence == sequence && execution.Terminal(r.Report.State) {
			r.Acknowledged = true
			return j.put(tx, "operations", id.String(), r)
		}
		return nil
	})
}

// Mutate preserves cancellation learned concurrently by the transport loop.
func (j *Journal) Mutate(id uuid.UUID, fn func(*Record)) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if e := j.get(tx, "operations", id.String(), &r); e != nil {
			return e
		}
		fn(&r)
		r.Report.Sequence++
		return j.put(tx, "operations", id.String(), r)
	})
}
