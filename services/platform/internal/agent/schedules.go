package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type scheduleState struct {
	Installation backup.Installation
	Runs         []backup.Run
	Sequence     int64
	Writers      []string
}

func (j *Journal) scheduleIn(tx *bolt.Tx, id string) (scheduleState, error) {
	var legacy, advanced scheduleState
	oldErr := j.get(tx, "schedules", id, &legacy)
	newErr := j.get(tx, "schedules-v2", id, &advanced)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return legacy, oldErr
	}
	if newErr != nil && !errors.Is(newErr, os.ErrNotExist) {
		return advanced, newErr
	}
	if newErr != nil {
		return legacy, oldErr
	}
	if oldErr == nil && (legacy.Installation.Policy.Generation > advanced.Installation.Policy.Generation || legacy.Installation.Policy.Generation == advanced.Installation.Policy.Generation && legacy.Sequence > advanced.Sequence) {
		return legacy, nil
	}
	return advanced, nil
}
func (j *Journal) schedule(id uuid.UUID) (scheduleState, error) {
	var v scheduleState
	err := j.db.View(func(tx *bolt.Tx) error { var err error; v, err = j.scheduleIn(tx, id.String()); return err })
	return v, err
}
func (j *Journal) saveSchedule(v scheduleState) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.putSchedule(tx, v) })
}
func (j *Journal) putSchedule(tx *bolt.Tx, v scheduleState) error {
	if err := trimRunHistory(&v, 128); err != nil {
		return err
	}
	id := v.Installation.Policy.ID.String()
	old, err := j.scheduleIn(tx, id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	v.Sequence = old.Sequence + 1
	if v.Installation.Policy.GeneralSchedule() || v.Installation.Policy.Coordinated {
		if err = j.put(tx, "schedules-v2", id, v); err != nil {
			return err
		}
		// Old binaries retain uncertainty/writer fences without executing the
		// advanced cadence. Newer legacy mutations still supersede this mirror.
		mirror := v
		mirror.Installation.Policy.Enabled = false
		mirror.Installation.Policy.Schedule = backup.Schedule{Timezone: "UTC"}
		mirror.Installation.Policy.TimeoutSeconds = 0
		mirror.Installation.Next = time.Time{}
		return j.put(tx, "schedules", id, mirror)
	}
	if err = j.put(tx, "schedules", id, v); err != nil {
		return err
	}
	return tx.Bucket([]byte("schedules-v2")).Delete([]byte(id))
}
func (j *Journal) eachSchedule(tx *bolt.Tx, visit func(string, scheduleState) error) error {
	seen := map[string]bool{}
	for _, bucket := range []string{"schedules-v2", "schedules"} {
		if err := tx.Bucket([]byte(bucket)).ForEach(func(k, v []byte) error {
			id := string(k)
			if seen[id] {
				return nil
			}
			seen[id] = true
			state, err := j.scheduleIn(tx, id)
			if err != nil {
				return err
			}
			return visit(id, state)
		}); err != nil {
			return err
		}
	}
	return nil
}
func (j *Journal) scheduleStates() (out []scheduleState, err error) {
	err = j.db.View(func(tx *bolt.Tx) error {
		return j.eachSchedule(tx, func(_ string, s scheduleState) error { out = append(out, s); return nil })
	})
	return
}
func (j *Journal) ScheduleReports() ([]api.ScheduleReport, error) {
	states, err := j.scheduleStates()
	if err != nil {
		return nil, err
	}
	out := []api.ScheduleReport{}
	for _, s := range states {
		status := backup.Status{Enabled: s.Installation.Policy.Enabled, PolicyID: s.Installation.Policy.ID, Generation: s.Installation.Policy.Generation, Next: s.Installation.Next, Runs: s.Runs}
		if status.Runs == nil {
			status.Runs = []backup.Run{}
		}
		raw, _ := json.Marshal(status)
		var dto api.BackupStatus
		if err = json.Unmarshal(raw, &dto); err != nil {
			return nil, err
		}
		out = append(out, api.ScheduleReport{Sequence: s.Sequence, Status: dto})
	}
	return out, nil
}
func (e *Engine) executeBackup(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var b backup.Command
	if c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Digest() != c.PlanHash || b.Installation.Policy.ID != c.ResourceId || b.Installation.Policy.ServerID != c.ServerId || b.Installation.Policy.EnvironmentID != c.EnvironmentId || b.Installation.Policy.Validate() != nil || (c.Kind == "schedule") != b.Installation.Policy.GeneralSchedule() {
		return e.finish(c.Id, "failed", "Backup command identity or policy is invalid.")
	}
	if e.cancelled(c.Id) || r.Checkpoint == "accepted" && time.Now().After(c.Deadline) {
		return e.finish(c.Id, "cancelled", "Cancelled before backup host effects.")
	}
	if r.Checkpoint != "accepted" {
		return e.finish(c.Id, "needs_attention", "An interrupted backup command needs local inspection. Scheduled run history records its outcome; it will not be repeated automatically.")
	}
	parent := ctx
	ctx, stop := e.commandContext(ctx, c)
	defer stop()
	if err := e.checkpoint(c.Id, "backup-effect"); err != nil {
		return err
	}
	var err error
	switch b.Action {
	case "install":
		old, readErr := e.Journal.schedule(c.ResourceId)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			err = readErr
			break
		}
		if readErr == nil && old.Installation.Policy.Generation > b.Installation.Policy.Generation {
			err = errors.New("stale policy")
			break
		}
		if readErr == nil && old.Installation.Policy.Generation == b.Installation.Policy.Generation {
			if backup.Hash(old.Installation.Policy) != backup.Hash(b.Installation.Policy) {
				err = errors.New("policy changed without generation")
			}
			break
		}
		if err = e.checkBackupSource(ctx, b.Installation.Policy); err != nil {
			break
		}
		if b.Installation.Policy.Method == "postgres-logical" {
			a, sourceErr := sourceApp(b.Installation.Policy)
			if sourceErr != nil {
				err = sourceErr
				break
			}
			source, sourceErr := e.sourceContainer(ctx, b.Installation.Policy, a.ID)
			if sourceErr != nil {
				err = sourceErr
				break
			}
			for _, value := range source.Config.Env {
				if password, ok := strings.CutPrefix(value, "POSTGRES_PASSWORD="); ok {
					b.Installation.Access.DatabasePassword = password
				}
			}
			if b.Installation.Access.DatabasePassword == "" {
				err = errors.New("database initialization credential unavailable")
				break
			}
		}
		if b.Installation.Policy.Method != "container-job" {
			err = e.prepareRepository(ctx, &b.Installation, true)
			if err != nil {
				break
			}
		}
		b.Installation.Next = b.Installation.Policy.Schedule.Next(time.Now())
		if b.Installation.Next.IsZero() {
			err = errors.New("schedule has no supported next occurrence")
			break
		}
		next := scheduleState{Installation: b.Installation, Runs: old.Runs, Sequence: old.Sequence}
		if len(old.Writers) > 0 || hasUncertainRun(old.Runs) {
			err = errors.New("resolve interrupted run before replacing schedule")
			break
		}
		err = e.Journal.saveSchedule(next)
	case "disable":
		var s scheduleState
		s, err = e.Journal.schedule(c.ResourceId)
		if err == nil {
			s.Installation.Policy.Enabled = false
			err = e.Journal.saveSchedule(s)
		}
	case "check":
		err = e.prepareRepository(ctx, &b.Installation, false)
	case "backup", "restore":
		var s scheduleState
		s, err = e.Journal.schedule(c.ResourceId)
		if err != nil {
			break
		}
		if s.Installation.Policy.Generation != b.Installation.Policy.Generation || s.Installation.Policy.GeneralSchedule() != b.Installation.Policy.GeneralSchedule() {
			err = errors.New("install current policy first")
			break
		}
		at := time.Now().UTC()
		if b.ScheduledAt != nil {
			if b.Action != "backup" || !b.Installation.Policy.Coordinated || b.RunID != backup.RunID(b.Installation.Policy, *b.ScheduledAt) || b.ScheduledAt.After(at) {
				err = errors.New("invalid coordinated schedule occurrence")
				break
			}
			at = b.ScheduledAt.UTC()
		}
		err = e.runScheduled(ctx, &s, at, b.RunID, b.Snapshot, b.TargetID)
	default:
		err = errors.New("unsupported backup action")
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if ctx.Err() != nil {
		return e.finish(c.Id, "cancelled", "Backup command cancelled or expired. Inspect any interrupted run before retrying.")
	}
	if err != nil {
		return e.finish(c.Id, "failed", "Backup action failed. Check policy compatibility, storage access, available capacity and the run history. No existing data was replaced.")
	}
	return e.finish(c.Id, "succeeded", "Backup action completed. Snapshot upload and restore verification are recorded separately. Cutover requires a new deployment review.")
}
func hasUncertainRun(runs []backup.Run) bool {
	for _, r := range runs {
		if r.State == "running" || r.State == "needs_attention" {
			return true
		}
	}
	return false
}

// TickSchedules is independent of management connectivity. It shares the host
// execution lock with deployments and persists each run before external effects.
func (e *Engine) TickSchedules(ctx context.Context) error { return e.tickSchedules(ctx, time.Now) }
func (e *Engine) tickSchedules(ctx context.Context, clock func() time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if held, err := e.Journal.recoveryHeld(); err != nil {
		return err
	} else if held {
		return nil
	}
	if blocked, err := e.Journal.hasCrossHostBarrier(); err != nil {
		return err
	} else if blocked {
		return nil
	}
	upgrades, err := e.Journal.upgrades()
	if err != nil {
		return err
	}
	for _, u := range upgrades {
		if u.Status.Active() {
			return nil
		}
	}

	states, err := e.Journal.scheduleStates()
	if err != nil {
		return err
	}
	pending, err := e.Journal.Pending()
	if err != nil {
		return err
	}
	for _, r := range pending {
		if !execution.Terminal(r.Report.State) {
			return nil
		}
	}
	blocked := false
	for i := range states {
		s := &states[i]
		if len(s.Writers) > 0 {
			if err = e.resumeBackupWriters(ctx, s); err != nil {
				return err
			}
		}
		changed := false
		for i := range s.Runs {
			if s.Runs[i].State == "running" {
				s.Runs[i].State = "needs_attention"
				s.Runs[i].Note = "The agent restarted during this run. Inspect its resources before authorizing another policy generation."
				changed = true
			}
		}
		if changed {
			if err = e.Journal.saveSchedule(*s); err != nil {
				return err
			}
		}
		blocked = blocked || hasUncertainRun(s.Runs)
	}
	if blocked {
		return nil
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Installation.Next.Before(states[j].Installation.Next) })
	for _, s := range states {
		now := clock().UTC()
		p := s.Installation.Policy
		if p.Coordinated {
			continue
		}
		if p.RequiresQuiescence() {
			active, err := e.Journal.unit(p.Plan.UnitID)
			if err != nil {
				return err
			}
			if active.Plan.Specification.HasRemotePlacement() {
				continue
			}
		}
		if !p.Enabled || hasUncertainRun(s.Runs) || s.Installation.Next.IsZero() || now.Before(s.Installation.Next) {
			continue
		}
		at, future, dueErr := p.Schedule.Due(s.Installation.Next, now)
		if dueErr != nil {
			return dueErr
		}
		s.Installation.Next = future
		if p.Method == "container-job" && now.Sub(at) > time.Minute {
			end := now
			if err = trimRunHistory(&s, 127); err != nil {
				return err
			}
			s.Runs = append(s.Runs, backup.Run{ID: backup.RunID(p, at), PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: now, FinishedAt: &end, State: "skipped", Note: "Missed container job skipped; no external effects were replayed."})
			if err = e.Journal.saveSchedule(s); err != nil {
				return err
			}
			continue
		}
		if err = e.runScheduled(ctx, &s, at, backup.RunID(p, at), "", uuid.Nil); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if hasUncertainRun(s.Runs) {
			return nil
		}
	}
	return nil
}
func (e *Engine) runScheduled(ctx context.Context, s *scheduleState, at time.Time, id uuid.UUID, snapshot string, target uuid.UUID) error {
	if hasUncertainRun(s.Runs) {
		return errors.New("inspect interrupted run first")
	}
	for _, r := range s.Runs {
		if r.ID == id {
			return nil
		}
	}
	if err := trimRunHistory(s, 127); err != nil {
		return err
	}
	if snapshot == "" {
		target = uuid.Nil
	}

	r := backup.Run{ID: id, PolicyID: s.Installation.Policy.ID, Generation: s.Installation.Policy.Generation, ScheduledAt: at, StartedAt: time.Now().UTC(), State: "running", TargetID: target, Snapshot: snapshot}
	s.Runs = append(s.Runs, r)
	if err := e.Journal.saveSchedule(*s); err != nil {
		return err
	}
	e.point("backup-run-recorded")
	index := len(s.Runs) - 1
	timeout := s.Installation.Policy.RunTimeout()
	if snapshot != "" {
		timeout = 30 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	if snapshot != "" {
		s.Runs[index].Protected = true
		for i := range s.Runs {
			if s.Runs[i].Snapshot == snapshot {
				s.Runs[i].Protected = true
			}
		}
		if err = e.Journal.saveSchedule(*s); err == nil {
			err = e.restoreSnapshot(runCtx, s, &r)
		}
	} else if s.Installation.Policy.Method == "container-job" {
		err = e.runContainerJob(runCtx, s, &r)
	} else {
		err = e.backupSnapshot(runCtx, s, &r)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	end := time.Now().UTC()
	r.FinishedAt = &end
	r.State = "succeeded"
	r.Note = "Snapshot uploaded; a restore test is still required."
	if s.Installation.Policy.Method == "container-job" {
		r.Note = "Container job finished; exit result recorded."
	}
	if r.Verified {
		r.Note = "Fresh restore verified. Existing services and data remain in use until a separate reviewed deployment."
	}
	if err != nil {
		r.State = "failed"
		if s.Installation.Policy.Method == "container-job" && (r.ExitCode == nil || errors.Is(err, errJobCleanup)) {
			r.State = "needs_attention"
		}
		r.Note = "Run failed. Inspect storage access, supported source state and available capacity. Incomplete output is not a usable backup."
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			r.Note = "Run exceeded its configured timeout. Inspect the outcome before retrying."
			if s.Installation.Policy.Method == "container-job" && r.ExitCode != nil {
				r.Note = "Container job exceeded its timeout; its owned container was stopped and the exit result recorded."
			}
		}
		if errors.Is(err, errJobCleanup) {
			r.Note = "Job cleanup could not be confirmed. Inspect the owned run container and resolve the run locally before further host operations."
		}
	}
	compactManifest(&r)
	s.Runs[index] = r
	if snapshot != "" && err == nil {
		for i := range s.Runs {
			if s.Runs[i].Snapshot == snapshot {
				s.Runs[i].Verified = true
				s.Runs[i].Protected = false
			}
		}
	}
	if saveErr := e.Journal.saveSchedule(*s); saveErr != nil {
		return saveErr
	}
	return err
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
			return e.Journal.saveSchedule(s)
		}
	}
	return errors.New("run is not awaiting local inspection")
}

func compactManifest(r *backup.Run) {
	if r.Manifest == nil {
		return
	}
	m := *r.Manifest
	if r.ManifestHash == "" {
		r.ManifestHash = backup.Hash(m)
	}
	m.Roles = []backup.Role{}
	m.Extensions = []string{}
	r.Manifest = &m
}

// Keep a representative for every retained snapshot, while ordinary run history
// is bounded independently of how often a snapshot is restore-tested.
func trimRunHistory(s *scheduleState, limit int) error {
	for i := range s.Runs {
		compactManifest(&s.Runs[i])
	}
	for len(s.Runs) > limit {
		counts := map[string]int{}
		for _, r := range s.Runs {
			if r.Snapshot != "" && !r.Pruned {
				counts[r.Snapshot]++
			}
		}
		drop := -1
		for i, r := range s.Runs {
			if r.Protected || r.State == "running" || r.State == "needs_attention" {
				continue
			}
			if r.Snapshot == "" || r.Pruned || counts[r.Snapshot] > 1 {
				drop = i
				break
			}
		}
		if drop < 0 {
			return errors.New("run history is full of protected snapshots")
		}
		s.Runs = append(s.Runs[:drop], s.Runs[drop+1:]...)
	}
	return nil
}
