package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// RejectCrossHostEffect records an inspected uncertain effect as failed. Only
// the management parent can subsequently resume writers or remove candidates.
// It never declares success, replays an effect or releases the host barrier.
func (e *Engine) RejectCrossHostEffect(ctx context.Context, id uuid.UUID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.Journal.Record(id)
	if err != nil {
		return err
	}
	s, err := e.Journal.crossHost()
	if err != nil {
		return err
	}
	if r.Report.State != "needs_attention" || r.Command.ParentId == nil || *r.Command.ParentId != s.ParentID || s.ParentID == uuid.Nil {
		return errors.New("the operation must require attention under this host's frozen parent")
	}
	var schedule *scheduleState
	if r.Command.Kind == "backup" || r.Command.Kind == "schedule" {
		var b backup.Command
		if r.Command.Backup == nil || json.Unmarshal(*r.Command.Backup, &b) != nil || b.Action != "backup" || s.Maintenance == nil || s.Maintenance.Kind != "backup" || !maintenanceLeafMatches(r.Command, s) {
			return errors.New("only the frozen coordinated backup may be rejected")
		}
		stored, err := e.Journal.schedule(b.Installation.Policy.ID)
		if err != nil {
			return err
		}
		if len(stored.Writers) != 0 || backup.Hash(stored.Installation.Policy) != backup.Hash(b.Installation.Policy) {
			return errors.New("the backup source or local writer ownership changed")
		}
		found := false
		for i := range stored.Runs {
			run := &stored.Runs[i]
			if run.ID == b.RunID && run.PolicyID == b.Installation.Policy.ID && run.Generation == b.Installation.Policy.Generation && (run.State == "running" || run.State == "needs_attention") {
				found = true
				at := time.Now().UTC()
				run.State, run.FinishedAt = "failed", &at
				run.Note = "Local operator rejected the interrupted run after inspection; no success was inferred."
			}
		}
		if !found {
			return errors.New("the frozen run is not awaiting local inspection")
		}
		list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelServer+"="+r.Command.ServerId.String(), "com.astilba.run="+b.RunID.String())})
		if err != nil {
			return err
		}
		for _, container := range list.Items {
			if container.State == "running" || container.State == "restarting" || container.State == "paused" {
				return errors.New("inspect and stop the run's remaining containers before rejecting its outcome")
			}
		}
		schedule = &stored
	} else {
		command, err := crossHostCommand(r.Command)
		if err != nil || command.Phase != "hook" || command.Unit.Plan.Release == nil || s.Maintenance != nil || s.Phase != "hook" || s.Publication || s.Hook.OperationID != id || s.Hook.Plan.Hash != command.Unit.Plan.Hash {
			return errors.New("only the frozen unpublished release command may be rejected")
		}
		v, err := e.Docker.ContainerInspect(ctx, releaseCommandName(r.Command), client.ContainerInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		if err == nil {
			labels := v.Container.Config.Labels
			if labels[labelServer] != command.Unit.Plan.ServerID.String() || labels[labelEnvironment] != command.Unit.Plan.EnvironmentID.String() || labels["com.astilba.unit"] != command.Unit.Plan.UnitID.String() || labels[labelKind] != "release-command-v1" || labels["com.astilba.operation"] != id.String() || labels["com.astilba.plan"] != command.Unit.Plan.Hash || labels[labelResource] != command.Unit.Plan.Release.ApplicationID.String() || v.Container.Config.Image != command.Unit.Plan.Images[command.Unit.Plan.Release.ApplicationID.String()] || v.Container.State.Running || v.Container.State.Paused || v.Container.State.Restarting {
				return errors.New("inspect and stop the exact owned release command before rejecting its outcome")
			}
		}
	}
	return e.Journal.db.Update(func(tx *bolt.Tx) error {
		if err := e.Journal.checkController(tx, r.Command); err != nil {
			return err
		}
		if err := e.Journal.crossHostAdmission(tx, r.Command); err != nil {
			return err
		}
		if schedule != nil {
			if err := e.Journal.putSchedule(tx, *schedule); err != nil {
				return err
			}
		}
		r.Report.State = "failed"
		r.Report.Note = "Local operator rejected the uncertain effect after inspection. It was not replayed; external writes and retained data remain. The parent must confirm recovery."
		r.Report.Sequence++
		return e.Journal.put(tx, "operations", id.String(), r)
	})
}

type CrossHostView struct {
	ParentID     uuid.UUID `json:"parentId"`
	GroupHash    string    `json:"groupHash"`
	Controller   int64     `json:"controllerGeneration"`
	UnitID       uuid.UUID `json:"unitId"`
	PlanHash     string    `json:"planHash"`
	Phase        string    `json:"phase"`
	Publication  bool      `json:"publicationConfirmed"`
	Maintenance  string    `json:"maintenance,omitempty"`
	EffectAction string    `json:"effectAction,omitempty"`
	WriterCount  int       `json:"recordedWriters"`
}

// CrossHostView exposes recovery identities without command bodies or secrets.
func (j *Journal) CrossHostView() (CrossHostView, error) {
	s, err := j.crossHost()
	v := CrossHostView{ParentID: s.ParentID, GroupHash: s.GroupHash, Controller: s.Controller, UnitID: s.Unit.Plan.UnitID, PlanHash: s.Unit.Plan.Hash, Phase: s.Phase, Publication: s.Publication, EffectAction: s.EffectAction}
	if s.Maintenance != nil {
		v.Maintenance = s.Maintenance.Kind
	}
	if s.Pause != nil {
		v.WriterCount = len(s.Pause.Writers)
	}
	return v, err
}

// Retry re-enters the same command after local inspection; it never releases a
// host barrier or adopts a replacement plan. Executors retain effect-specific
// rules, including refusing to replay an uncertain release command or data run.
func (j *Journal) Retry(id uuid.UUID) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var r Record
		if err := j.get(tx, "operations", id.String(), &r); err != nil {
			return err
		}
		if j.isCrossHostSentinel(tx, id.String()) || j.isDistributedFence(tx, id.String()) || j.isRecoverySentinel(tx, id.String()) {
			return errors.New("a compatibility barrier cannot be retried as an operation")
		}
		if r.Report.State != "needs_attention" {
			return errors.New("only an operation requiring attention can be retried")
		}
		if err := j.checkController(tx, r.Command); err != nil {
			return err
		}
		if err := j.crossHostAdmission(tx, r.Command); err != nil {
			return err
		}
		r.Report.State = "running"
		r.Report.Note = "Local operator requested reconciliation of the same recorded command."
		r.Report.Sequence++
		return j.put(tx, "operations", id.String(), r)
	})
}
