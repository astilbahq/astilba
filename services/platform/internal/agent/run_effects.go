package agent

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

// hasUncertainRun reports a run whose outcome is unknown after it may have changed something.
// One the agent stopped before its first effect is not: it is settled as failed before another
// run starts.
func hasUncertainRun(s scheduleState) bool {
	for _, r := range s.Runs {
		if r.State == "needs_attention" || r.State == "running" && r.ID != s.BeforeEffects {
			return true
		}
	}
	return false
}

// interruptedRunFailed reports whether an interrupted backup or restore command's run is known to
// have failed: never recorded, recorded as failed, or stopped before its first effect, which it
// now records as failed.
func (e *Engine) interruptedRunFailed(b backup.Command) (bool, error) {
	if b.RunID == uuid.Nil {
		return false, nil
	}
	s, err := e.Journal.schedule(b.Installation.Policy.ID)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, run := range s.Runs {
		if run.ID != b.RunID {
			continue
		}
		if run.State == "failed" {
			return true, nil
		}
		if run.State != "running" || s.BeforeEffects != b.RunID {
			return false, nil
		}
		settleUnstarted(&s, time.Now().UTC())
		return true, e.Journal.saveSchedule(s)
	}
	return true, nil
}

// settleUnstarted fails the run the agent stopped before its first effect. Only a caller that
// holds the engine's lock, with no run in progress, may call it.
func settleUnstarted(s *scheduleState, now time.Time) bool {
	if s.BeforeEffects == uuid.Nil {
		return false
	}
	for i := range s.Runs {
		if s.Runs[i].ID == s.BeforeEffects && s.Runs[i].State == "running" {
			// As a run that fails in process, a restore no longer protects its own record.
			s.Runs[i].State, s.Runs[i].FinishedAt, s.Runs[i].Protected = "failed", &now, false
			s.Runs[i].Note = "The agent stopped before this run changed anything. It will not be repeated automatically."
		}
	}
	s.BeforeEffects = uuid.Nil
	return true
}

// beginEffects records that run r may now change things outside the journal. Until it returns
// nil, nothing of the run has happened; a cancelled run begins nothing.
func (e *Engine) beginEffects(ctx context.Context, s *scheduleState, r *backup.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.BeforeEffects != r.ID {
		return nil
	}
	s.BeforeEffects = uuid.Nil
	if err := e.Journal.saveSchedule(*s); err != nil {
		s.BeforeEffects = r.ID
		return err
	}
	e.point("backup-effects-begun")
	return nil
}
