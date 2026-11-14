package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
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

// interruptedRunOutcome settles an interrupted backup or restore command by what its run did:
// failed when it was never recorded, recorded as failed, or stopped before its first effect
// (which it now records as failed); succeeded when its run succeeded before the command could
// finish; and needs_attention otherwise.
func (e *Engine) interruptedRunOutcome(b backup.Command) (api.ExecutionState, error) {
	if b.RunID == uuid.Nil {
		return "needs_attention", nil
	}
	s, err := e.Journal.schedule(b.Installation.Policy.ID)
	if errors.Is(err, os.ErrNotExist) {
		return "failed", nil
	}
	if err != nil {
		return "", err
	}
	for _, run := range s.Runs {
		if run.ID != b.RunID {
			continue
		}
		if run.State == "failed" || run.State == "succeeded" {
			return api.ExecutionState(run.State), nil
		}
		if run.State != "running" || s.BeforeEffects != b.RunID {
			return "needs_attention", nil
		}
		settleUnstarted(&s, time.Now().UTC())
		return "failed", e.Journal.saveSchedule(s)
	}
	return "failed", nil
}

// settleUnstarted fails the run the agent stopped before its first effect. Only a caller that
// holds the engine's lock, with no run in progress, may call it.
func settleUnstarted(s *scheduleState, now time.Time) bool {
	if s.BeforeEffects == uuid.Nil {
		return false
	}
	for i := range s.Runs {
		if s.Runs[i].ID == s.BeforeEffects && s.Runs[i].State == "running" {
			s.Runs[i].State, s.Runs[i].FinishedAt = "failed", &now
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

// errRunUncertain marks a run that ended needs_attention: it may have changed things that nothing
// confirmed, so it holds the host until it is resolved or, under a coordinated reservation,
// rejected.
var errRunUncertain = errors.New("the run's outcome needs local inspection")

const interruptedBackupNote = "An interrupted backup command needs local inspection. Scheduled run history records its outcome; it will not be repeated automatically."

// runCleanupLeft says what a run that began changing things may have left running, or "" once
// nothing it started can be. A process it ran in a container it doesn't own was confirmed ended
// where it ran (processLeft). Every container on this server labelled with the run, including one
// whose create failed as the run stopped, is force-removed and confirmed gone. restic needs no
// check: the agent waits for it to exit, and kills it when the run stops.
func (e *Engine) runCleanupLeft(p backup.Policy, run uuid.UUID, err error) string {
	var left []string
	if process := (*processLeft)(nil); errors.As(err, &process) {
		left = append(left, process.what)
	}
	if containers := e.removeRunContainers(p, run); containers != "" {
		left = append(left, containers)
	}
	return strings.Join(left, "; ")
}

func (e *Engine) removeRunContainers(p backup.Policy, run uuid.UUID) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filter := make(client.Filters).Add("label", labelServer+"="+p.ServerID.String(), "com.astilba.run="+run.String())
	// A container already being removed may take a moment to go.
	for attempt := 0; ; attempt++ {
		list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
		if err != nil {
			return "Docker could not list the run's containers"
		}
		if len(list.Items) == 0 {
			return ""
		}
		if attempt == 5 || attempt > 0 && !wait(ctx, time.Second) {
			names := make([]string, 0, len(list.Items))
			for _, c := range list.Items {
				names = append(names, containerName(c))
			}
			return "container " + strings.Join(names, ", ") + " could not be removed"
		}
		for _, c := range list.Items {
			_, _ = e.Docker.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
		}
	}
}

// processLeft is a run's error when a process it started in a container it doesn't own, such as
// pg_dump in its source database, could not be confirmed ended.
type processLeft struct{ what string }

func (p *processLeft) Error() string { return p.what }

const backupSucceededNote = "Backup action completed. Snapshot upload and restore verification are recorded separately. Cutover requires a new deployment review."

// commitSchedule writes the schedule an install or disable produces together with the command's
// success, so the agent never holds one without the other: a command stopped before it commits
// changed nothing and runs again, and one that committed is finished.
func (e *Engine) commitSchedule(id uuid.UUID, s scheduleState) error {
	return e.Journal.db.Update(func(tx *bolt.Tx) error {
		if err := e.Journal.putSchedule(tx, s); err != nil {
			return err
		}
		var r Record
		if err := e.Journal.get(tx, "operations", id.String(), &r); err != nil {
			return err
		}
		settleReport(&r, "succeeded", backupSucceededNote)
		r.Report.Sequence++
		return e.Journal.put(tx, "operations", id.String(), r)
	})
}

// ResolveScheduledRun is a local operator action after inspecting an uncertain
// run. It never declares success or removes retained data.
func (e *Engine) ResolveScheduledRun(ctx context.Context, policy, run uuid.UUID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.Journal.RequireUnreservedHost(); err != nil {
		return err
	}
	s, err := e.Journal.schedule(policy)
	if err != nil {
		return err
	}
	if s.Installation.Policy.RequiresQuiescence() {
		active, err := e.Journal.unit(s.Installation.Policy.Plan.UnitID)
		if err != nil {
			return err
		}
		if active.Plan.Specification.HasRemotePlacement() {
			return errors.New("distributed writer recovery requires coordinated maintenance")
		}
	}
	if err = e.resumeBackupWriters(ctx, &s); err != nil {
		return err
	}
	list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelServer+"="+s.Installation.Policy.ServerID.String(), "com.astilba.run="+run.String())})
	if err != nil {
		return err
	}
	for _, c := range list.Items {
		if c.State == "running" {
			return errors.New("run still has an active container; inspect and stop it before resolving")
		}
	}
	for i := range s.Runs {
		if s.Runs[i].ID == run && (s.Runs[i].State == "running" || s.Runs[i].State == "needs_attention") {
			s.Runs[i].State = "failed"
			s.Runs[i].Note = "Local operator resolved the interrupted run after inspection; no successful recovery was inferred."
			if s.BeforeEffects == run {
				s.BeforeEffects = uuid.Nil
			}
			return e.Journal.db.Update(func(tx *bolt.Tx) error {
				if err := e.Journal.putSchedule(tx, s); err != nil {
					return err
				}
				return e.Journal.failRunCommands(tx, policy, run, "Local operator resolved the command's interrupted run after inspection; no success was inferred.")
			})
		}
	}
	return errors.New("run is not awaiting local inspection")
}

// failRunCommands records as failed each backup or restore command still awaiting inspection for
// a run now known to have failed, so its unsettled record no longer holds the host's schedules.
func (j *Journal) failRunCommands(tx *bolt.Tx, policy, run uuid.UUID, note string) error {
	var settle []Record
	if err := tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
		if j.isCrossHostSentinel(tx, string(k)) || j.isDistributedFence(tx, string(k)) || j.isRecoverySentinel(tx, string(k)) {
			return nil
		}
		var r Record
		if err := j.get(tx, "operations", string(k), &r); err != nil {
			return err
		}
		var b backup.Command
		if r.Report.State != "needs_attention" || r.Command.Kind != "backup" && r.Command.Kind != "schedule" || r.Command.Backup == nil || json.Unmarshal(*r.Command.Backup, &b) != nil {
			return nil
		}
		if (b.Action == "backup" || b.Action == "restore") && b.RunID == run && b.Installation.Policy.ID == policy {
			settle = append(settle, r)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range settle {
		settleReport(&r, "failed", note)
		r.Report.Sequence++
		if err := j.put(tx, "operations", r.Command.Id.String(), r); err != nil {
			return err
		}
	}
	return nil
}

// containerName is a listed container's name, or its short ID when it has none.
func containerName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID[:min(12, len(c.ID))]
}
