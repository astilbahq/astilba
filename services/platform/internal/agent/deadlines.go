package agent

import (
	"errors"
	"os"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deadline"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// runsLate says whether a command first taken after its admission deadline still runs. Its rule
// must allow it, and the work it ends must be what this host's journal holds: the command's claim
// is never enough. While a reservation is held, Accept has already refused any coordinated command
// that doesn't match it exactly (crossHostAdmission), so holding one here means holding this one. A command that runs late changes nothing beyond what its executor allows for
// the held work, and starts nothing.
func (e *Engine) runsLate(c api.ExecutionCommand) (bool, error) {
	rule := deadline.Of(c)
	switch rule {
	case deadline.NarrowsAccess:
		return true, nil
	case deadline.ExtendsHeldRoot:
		d, err := commandCheckpoint(c)
		if err != nil || d.Sequence == 0 {
			return false, nil
		}
		head, err := e.Journal.recoveryHead(d.ParentID)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil && head.Descriptor.ParentID != uuid.Nil && deployment.SameRecoveryRoot(head.Descriptor, d), err
	case deadline.EndsHeldWork, deadline.EndsHeldWorkOrNone:
		held, err := e.Journal.crossHost()
		if err != nil {
			return false, err
		}
		return held.ParentID != uuid.Nil || rule == deadline.EndsHeldWorkOrNone, nil
	case deadline.RetiresHeldUnit:
		return e.Journal.holdsUnit(c)
	}
	return false, nil
}

// lateNote says why a command ended unrun at its deadline. When only management's clock had
// passed it, this host's clock is behind management's, which its owner should correct.
func lateNote(c api.ExecutionCommand) string {
	if time.Now().After(c.Deadline) {
		return "The admission deadline passed before execution."
	}
	return "The admission deadline passed before execution by management's clock. This host's clock is behind management's."
}

// workEnd is when the work of a command this host took up must end: lifetime after it was first
// taken up, and never before its issue deadline, whichever clock is ahead. A record an older agent
// began has no start and keeps its deadline.
func (r Record) workEnd(lifetime time.Duration) time.Time {
	if r.StartedAt == nil || r.StartedAt.Add(lifetime).Before(r.Command.Deadline) {
		return r.Command.Deadline
	}
	return r.StartedAt.Add(lifetime)
}

// startWork records when this host first takes a command up, unless an earlier execution already
// began it, and returns the time it was first taken up. A record an older agent began has none.
// It changes nothing management reads, so the report's sequence stays.
func (j *Journal) startWork(id uuid.UUID) (*time.Time, error) {
	var started *time.Time
	err := j.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if e := j.get(tx, "operations", id.String(), &r); e != nil {
			return e
		}
		if r.StartedAt == nil && r.Checkpoint == "accepted" {
			now := time.Now().UTC()
			r.StartedAt = &now
			if e := j.put(tx, "operations", id.String(), r); e != nil {
				return e
			}
		}
		started = r.StartedAt
		return nil
	})
	return started, err
}
