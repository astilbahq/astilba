package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	PreviewOnly          bool
	Enrolled             bool
	Role                 api.ServerRole
	PublicKey            []byte
	// Key is the agent's private key, sealed to this machine. A key-bound agent has no bearer.
	Key []byte `json:",omitempty"`
	// PendingRotation is the successor key while its rotation is unconfirmed.
	PendingRotation []byte `json:",omitempty"`
	// BindKey is the key a bind registers. It is kept beside the bearer until the key is in use.
	BindKey []byte `json:",omitempty"`
	// Enrollments are the outstanding enrollment codes by code hash, each with what was first sent
	// with it, so a retry presents what management may already hold.
	Enrollments map[string]Enrollment `json:",omitempty"`
	// EnrolledCode is the hash of the code that enrolled this identity. The agent never sends it
	// again, since management would read a new key with it as a second holder.
	EnrolledCode string `json:",omitempty"`
}

// Enrollment is one outstanding enrollment code.
type Enrollment struct {
	// Keyed codes send Key; legacy codes, for controllers without keys, send Credential.
	Keyed      bool
	Key        []byte `json:",omitempty"`
	Credential string `json:",omitempty"`
	// SealedCode is the code sealed like the key; Code is used only where nothing can seal.
	SealedCode []byte `json:",omitempty"`
	Code       string `json:",omitempty"`
	FirstSent  time.Time
}
type Record struct {
	Command      api.ExecutionCommand
	Report       api.AgentReport
	Checkpoint   string
	Acknowledged bool
	AcceptedAt   *time.Time                 `json:",omitempty"`
	Railpack     *pipeline.PreparedRailpack `json:",omitempty"`
}
type Journal struct {
	db *journalDB
	// keys seals and opens records. A rekey replaces it rather than changing it.
	keys          atomic.Pointer[cryptobox.Keyring]
	dir           string
	recoveryMu    sync.Mutex
	formatVersion atomic.Int64
	// stateMu serializes changes to keyring.json and to the keys the journal holds.
	stateMu sync.Mutex
	// sealing says the journal seals its keyring to this machine's key store once one works. store
	// is that store once it has, and from then on. Both are guarded by stateMu.
	sealing bool
	store   KeyStore
	// rekeyedAt is the transaction that last re-sealed every record.
	rekeyedAt      atomic.Uint64
	startupUpdates map[uuid.UUID]bool
}

// Open opens a journal whose keyring is never sealed, for tests and development. It refuses one
// that is.
func Open(dir string) (*Journal, error) { return openJournal(context.Background(), dir, nil, nil) }

// OpenSealed opens the journal with this machine's key store, which store opens. A keyring sealed to
// it needs the store; one in the clear is sealed to it now if the store works, or else once a rekey
// finds that it does.
func OpenSealed(ctx context.Context, dir string, store func(context.Context) (KeyStore, error)) (*Journal, error) {
	return openJournal(ctx, dir, store, nil)
}

func openJournal(ctx context.Context, dir string, store func(context.Context) (KeyStore, error), point func(string) error) (*Journal, error) {
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
	j := &Journal{db: &journalDB{DB: db}, dir: dir}
	if err = j.openLocalKeys(ctx, store, point); err != nil {
		return fail(err)
	}
	// Only a clean close vouches that nothing wrote since: not an earlier agent, which forgets
	// commands without recording it, and not this one cut short.
	clean := false
	if err = db.View(func(tx *bolt.Tx) error {
		var closed int64
		e := j.get(tx, "identity", "closed-at", &closed)
		clean = e == nil && closed == int64(tx.ID())
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return e
	}); err != nil {
		return fail(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range []string{"identity", "operations", "generations", "units", "routing", "schedules", "schedules-v2", "upgrades", "releases", "preview-cleanups", "cross-host", "distributed-fences", "retained-pools", "recovery-heads", "recovery-checkpoints", "recovery-authority", "recovery-gc", "troubleshooting-shells", "troubleshooting-entered"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(b)); e != nil {
				return e
			}
		}
		if err := j.dropSubmissionCredentials(tx); err != nil {
			return err
		}
		if err := j.redactFinished(tx); err != nil {
			return err
		}
		if !clean {
			return j.seedForgotten(tx)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if err = j.pruneRecovery(); err != nil {
		return fail(err)
	}
	j.formatVersion.CompareAndSwap(0, localStateVersion)
	j.startupUpdates = map[uuid.UUID]bool{}
	pending, err := j.Pending()
	if err != nil {
		return fail(err)
	}
	for _, record := range pending {
		if record.Command.Kind == "update" && record.Report.State == "running" {
			j.startupUpdates[record.Command.Id] = true
		}
	}
	return j, nil
}

// Close records the transaction it closes at, so the next open can tell whether anything else
// wrote in between. Every later commit, even an empty one, moves past it.
func (j *Journal) Close() error {
	mark := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "identity", "closed-at", int64(tx.ID())) })
	return errors.Join(mark, j.db.Close())
}

// dropSubmissionCredentials removes the repository write credential an earlier agent kept with each
// submission it was sent. Management now submits drafts itself, so a runner holds none at rest.
// It runs once; Accept drops one arriving later.
func (j *Journal) dropSubmissionCredentials(tx *bolt.Tx) error {
	const done = "submission-credentials-dropped"
	var dropped bool
	if e := j.get(tx, "identity", done, &dropped); e == nil || !errors.Is(e, os.ErrNotExist) {
		return e
	}
	keys := []string{}
	if e := tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}); e != nil {
		return e
	}
	for _, k := range keys {
		var r Record
		// Sentinels and fences share the bucket and hold other values.
		if j.get(tx, "operations", k, &r) != nil {
			continue
		}
		dropped, changed, e := withoutSubmissionAccess(r.Command)
		if e != nil {
			return e
		}
		if changed {
			r.Command = dropped
			if e = j.put(tx, "operations", k, r); e != nil {
				return e
			}
		}
	}
	return j.put(tx, "identity", done, true)
}

// seedForgotten raises each forgotten ceiling to its counter, because any command up to it may have
// been forgotten without a record.
func (j *Journal) seedForgotten(tx *bolt.Tx) error {
	counters := map[string]int64{}
	if e := tx.Bucket([]byte("generations")).ForEach(func(k, _ []byte) error {
		if strings.HasPrefix(string(k), "forgotten:") {
			return nil
		}
		var generation int64
		if e := j.get(tx, "generations", string(k), &generation); e != nil {
			return e
		}
		counters[string(k)] = generation
		return nil
	}); e != nil {
		return e
	}
	for scope, generation := range counters {
		var ceiling int64
		if e := j.get(tx, "generations", "forgotten:"+scope, &ceiling); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e := j.put(tx, "generations", "forgotten:"+scope, max(ceiling, generation)); e != nil {
			return e
		}
	}
	return nil
}
func (j *Journal) get(tx *bolt.Tx, bucket, key string, out any) error {
	bucketValue := tx.Bucket([]byte(bucket))
	if bucketValue == nil {
		return os.ErrNotExist
	}
	b := bucketValue.Get([]byte(key))
	if b == nil {
		return os.ErrNotExist
	}
	plain, e := j.keys.Load().Open(b, bucket+":"+key)
	if e != nil {
		return e
	}
	return json.Unmarshal(plain, out)
}
func (j *Journal) put(tx *bolt.Tx, bucket, key string, value any) error {
	value = withoutReleasedValues(bucket, value)
	raw, e := stablejson.Marshal(value)
	if e != nil {
		return e
	}
	limit := 32768
	if bucket == "units" || bucket == "routing" || bucket == "schedules" || bucket == "schedules-v2" || bucket == "upgrades" || bucket == "releases" || bucket == "cross-host" || bucket == "retained-pools" || bucket == "recovery-authority" {
		limit = 524288
	}
	if record, ok := value.(Record); ok && (record.Command.Kind == "visual" || record.Command.Kind == "pipeline" || record.Command.Kind == "backup" || record.Command.Kind == "schedule" || record.Command.Kind == "upgrade" || record.Command.Kind == "network" || record.Command.Kind == "crosshost") {
		limit = 524288
	}
	if len(raw) > limit {
		return errors.New("journal record limit exceeded")
	}
	data, e := j.keys.Load().Seal(raw, bucket+":"+key)
	if e != nil {
		return e
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), data)
}

// NextStartCounter raises the counter an agent presents with each new session, in its own
// transaction, before the request goes out. A copy or a restored backup of this directory then
// presents a value management has already seen, or a lower one.
func (j *Journal) NextStartCounter() (int64, error) {
	var next int64
	err := j.db.Update(func(tx *bolt.Tx) error {
		var current int64
		if e := j.get(tx, "identity", "start-counter", &current); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		next = current + 1
		return j.put(tx, "identity", "start-counter", next)
	})
	return next, err
}

func (j *Journal) Config() (Config, error) {
	var c Config
	e := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "identity", "config", &c) })
	return c, e
}
func (j *Journal) SetConfig(c Config) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "identity", "config", c) })
}

// UpdateConfig changes the configuration in one transaction, so concurrent changes to other fields
// survive.
func (j *Journal) UpdateConfig(change func(*Config) error) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var c Config
		if e := j.get(tx, "identity", "config", &c); e != nil {
			return e
		}
		if e := change(&c); e != nil {
			return e
		}
		return j.put(tx, "identity", "config", c)
	})
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
			if !r.Acknowledged && !(j.isCrossHostSentinel(tx, string(k)) || j.isDistributedFence(tx, string(k)) || j.isRecoverySentinel(tx, string(k))) {
				out = append(out, r)
			}
			return nil
		})
	})
	return
}
func sameCommand(a, b api.ExecutionCommand) bool {
	if a.RecoveryCheckpoint != nil || b.RecoveryCheckpoint != nil {
		ad, ae := commandCheckpoint(a)
		bd, be := commandCheckpoint(b)
		if ae != nil || be != nil || deployment.Hash(ad) != deployment.Hash(bd) {
			return false
		}
		a.RecoveryCheckpoint, b.RecoveryCheckpoint = nil, nil
	}
	if a.Kind == "crosshost" && b.Kind == "crosshost" {
		ap, ae := crossHostCommand(a)
		bp, be := crossHostCommand(b)
		if ae != nil || be != nil || ap.Digest() != a.PlanHash || bp.Digest() != b.PlanHash {
			return false
		}
		a.Crosshost = nil
		b.Crosshost = nil
	}
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
	if (a.Kind == "backup" || a.Kind == "schedule") && a.Kind == b.Kind {
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
	// Whether a delivery brought its values says how it travelled, not what the command is: the
	// journal keeps the values it was first given.
	a.SecretRelease, b.SecretRelease = nil, nil
	aj, _ := stablejson.Marshal(a)
	bj, _ := stablejson.Marshal(b)
	return bytes.Equal(aj, bj)
}

// supersededNote ends a command the host received only after a later one for the same scope.
const supersededNote = "This host had already taken a later command for the same scope, so it did not run this one and changed nothing."

// forgottenCancelNote ends a cancelled command that this host may have run before it lost the record.
const forgottenCancelNote = "Cancelled without running it now. This host had already taken a later command for the same scope and has no record of this one, so an earlier delivery may have run it."

// generationScope names the counter that orders a command against others of its kind.
func generationScope(c api.ExecutionCommand) string {
	switch {
	case recoveryOpen(c):
		return "recovery-open:" + c.ParentId.String()
	case recoveryClose(c):
		return "recovery-close:" + c.ParentId.String()
	case c.Kind == "network":
		return "host-network"
	case c.Kind == "update":
		return "agent-update"
	}
	return c.EnvironmentId.String()
}

// forget deletes a finished record and remembers the highest generation forgotten in its scope. A
// command delivered again at or below it may have run here before, so it can't be called unrun.
// Restrictions don't count: one is always taken, so none is ever called unrun. Nor does a record
// from an earlier controller: none of its commands is taken again, and its generations may be
// above anything the current one has issued. The ceiling sits beside the counters, so whatever
// resets them resets it too.
func (j *Journal) forget(tx *bolt.Tx, key []byte, r Record) error {
	var cfg Config
	if e := j.get(tx, "identity", "config", &cfg); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if networkRestriction(r.Command) || commandGeneration(r.Command) != cfg.ControllerGeneration {
		return tx.Bucket([]byte("operations")).Delete(key)
	}
	scope := "forgotten:" + generationScope(r.Command)
	var ceiling int64
	if e := j.get(tx, "generations", scope, &ceiling); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if r.Command.Generation > ceiling {
		if e := j.put(tx, "generations", scope, r.Command.Generation); e != nil {
			return e
		}
	}
	return tx.Bucket([]byte("operations")).Delete(key)
}

func (j *Journal) Accept(c api.ExecutionCommand) (Record, error) {
	var out Record
	c, _, err := withoutSubmissionAccess(c)
	if err != nil {
		return out, err
	}
	err = j.db.Update(func(tx *bolt.Tx) error {
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
		if err := j.crossHostAdmission(tx, c); err != nil {
			return err
		}
		// Read before this acceptance forgets anything: the record it forgets isn't this command's.
		key := generationScope(c)
		var forgotten int64
		if e := j.get(tx, "generations", "forgotten:"+key, &forgotten); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		count := 0
		var oldest Record
		var oldestKey string
		if e := tx.Bucket([]byte("operations")).ForEach(func(k, v []byte) error {
			var r Record
			if e := j.get(tx, "operations", string(k), &r); e != nil {
				return e
			}
			if j.isCrossHostSentinel(tx, string(k)) || j.isDistributedFence(tx, string(k)) || j.isRecoverySentinel(tx, string(k)) {
				return nil
			}
			openingSameRoot := recoveryOpen(c) && c.ParentId != nil && r.Command.ParentId != nil && *c.ParentId == *r.Command.ParentId
			if !r.Acknowledged && !networkRestriction(c) && !openingSameRoot {
				return errors.New("a command still awaits completion or acknowledgement")
			}
			if r.Command.ParentId != nil && tx.Bucket([]byte("recovery-heads")).Get([]byte(r.Command.ParentId.String())) != nil {
				return nil
			}
			count++
			if oldestKey == "" && r.Acknowledged {
				oldest, oldestKey = r, string(k)
			}
			return nil
		}); e != nil {
			return e
		}
		if count >= 128 {
			if oldestKey == "" {
				return errors.New("journal contains too many unacknowledged commands")
			}
			if e := j.forget(tx, []byte(oldestKey), oldest); e != nil {
				return e
			}
		}
		var generation int64
		e := j.get(tx, "generations", key, &generation)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if c.Generation <= generation && !networkRestriction(c) {
			// This host ran it and has since forgotten it, or never ran it. Only a restored management
			// that wasn't fenced delivers a finished command again, and its outcome is unknown here.
			// Management cancelling it needs only that it doesn't run now, and its parent can then
			// end and release its peers.
			if c.Generation <= forgotten {
				if !c.CancelRequested {
					return errors.New("stale generation")
				}
				out = Record{Command: c, Report: api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}, Note: forgottenCancelNote}, Checkpoint: "superseded"}
				return j.put(tx, "operations", c.Id.String(), out)
			}
			// A later command for the same scope reached this host first, so this one must never run.
			// Refusing it would leave management delivering it on every poll, and whatever waits on
			// it, such as a deployment holding its peers, waiting for good. It ends here instead,
			// untouched, and the counter keeps the later command's value. Restrictions never end
			// this way: one is always taken, whatever its generation.
			out = Record{Command: c, Report: api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "failed", Steps: []api.ExecutionStep{}, Note: supersededNote}, Checkpoint: "superseded"}
			if c.CancelRequested {
				out.Report.State = "cancelled"
			}
			return j.put(tx, "operations", c.Id.String(), out)
		}
		if e := j.put(tx, "generations", key, max(generation, c.Generation)); e != nil {
			return e
		}
		out = Record{Command: c, Report: api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}, Checkpoint: "accepted"}
		if c.Kind == "update" {
			accepted := time.Now().UTC()
			out.AcceptedAt = &accepted
		}
		return j.put(tx, "operations", c.Id.String(), out)
	})
	return out, err
}
func (j *Journal) Acknowledge(id uuid.UUID, sequence int64) error {
	prune := false
	err := j.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if e := j.get(tx, "operations", id.String(), &r); e != nil {
			return e
		}
		if r.Report.Sequence == sequence && execution.Terminal(r.Report.State) {
			r.Acknowledged = true
			if r.Command.ParentId != nil && tx.Bucket([]byte("recovery-heads")).Get([]byte(r.Command.ParentId.String())) != nil {
				prune = true
				if err := j.completeRecoveryHead(tx, r); err != nil {
					return err
				}
			}
			if err := tx.Bucket([]byte("preview-cleanups")).Delete([]byte(id.String())); err != nil {
				return err
			}
			return j.put(tx, "operations", id.String(), r)
		}
		return nil
	})
	if err == nil && prune {
		err = j.pruneRecovery()
	}
	return err
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
