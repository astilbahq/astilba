package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// pausedWriter records an instance maintenance stopped (Running) or found stopped and so
// leaves stopped. A database is recorded only in the second case.
type pausedWriter struct {
	ContainerID string
	Running     bool
}
type writerPause struct {
	Serving map[string]string
	Writers map[string]pausedWriter
}

func validMaintenance(m *deployment.Maintenance, p deployment.Plan) bool {
	if m.ResourceID == uuid.Nil || len(m.EffectHash) != 64 || len(m.ExpectedState) != 64 || m.Kind != "backup" && m.Kind != "upgrade" && m.Kind != "restore" {
		return false
	}
	if m.Kind == "backup" {
		return m.RunID != uuid.Nil && m.Target == nil
	}
	if m.Kind == "restore" && (m.Restore == nil || m.Restore.Validate() != nil || m.Restore.SourcePlanID != p.Local.GroupID || m.ResourceID != m.Restore.PolicyID || m.RunID != m.Restore.RunID || m.EffectHash != deployment.Hash(*m.Restore)) {
		return false
	}
	return m.Target != nil && m.Target.Validate(true) == nil && m.Target.ServerID == p.ServerID && m.Target.EnvironmentID == p.EnvironmentID && m.Target.UnitID == p.UnitID
}

func maintenanceLeafMatches(c api.ExecutionCommand, s crossHostState) bool {
	m := s.Maintenance
	if m.Kind == "backup" && (c.Kind == "backup" || c.Kind == "schedule") && c.Backup != nil {
		var command backup.Command
		return json.Unmarshal(*c.Backup, &command) == nil && command.Action == "backup" && s.EffectAction == "backup" && command.RunID == m.RunID && command.Installation.Policy.ID == m.ResourceID && command.Installation.Policy.Plan.Hash == s.Unit.Plan.Hash && command.Digest() == m.EffectHash
	}
	if m.Kind == "upgrade" && c.Kind == "upgrade" && c.Upgrade != nil {
		var command upgrade.Command
		if json.Unmarshal(*c.Upgrade, &command) != nil || command.Action != s.EffectAction || command.Plan.ID != m.ResourceID || command.Plan.Source.Hash != s.Unit.Plan.Hash {
			return false
		}
		command.Action = ""
		return command.Digest() == m.EffectHash
	}
	return false
}

func (e *Engine) executeMaintenancePhase(ctx context.Context, c api.ExecutionCommand, command deployment.CrossHostCommand, s crossHostState) error {
	p := s.Unit.Plan
	fail := func(err error) error {
		return e.finishPhase(ctx, c.Id, "needs_attention", safeVisualError(err, command.Secrets))
	}
	// Nothing has stopped on this host, so the parent can resume every other host's writers
	// and complete, rather than waiting for someone to reconcile an uncertain outcome.
	refuse := func(err error) error {
		return e.finishPhase(ctx, c.Id, "failed", safeVisualError(err, command.Secrets))
	}
	switch command.Phase {
	case "reserve":
		if s.Maintenance.Kind == "restore" && p.Local.Primary {
			if _, err := e.validateLocalRestore(s); err != nil {
				return fail(err)
			}
		}
		if _, err := e.maintenanceWriters(ctx, s); err != nil {
			return refuse(err)
		}
		if err := e.recordUnready(ctx, c, &s); err != nil {
			return refuse(err)
		}
	case "restore":
		if s.Maintenance.Kind != "restore" || !p.Local.Primary || s.Phase != "paused" && s.Phase != "restoring" && s.Phase != "restored" {
			return fail(errors.New("restored-volume activation requires the frozen primary writer pause"))
		}
		if err := e.activateRestoreCutover(ctx, c, command.Secrets, &s); err != nil {
			return fail(err)
		}
	case "pause":
		if s.Phase != "reserved" && s.Phase != "pausing" && s.Phase != "paused" {
			return fail(errors.New("writer pause is out of phase"))
		}
		if s.Pause == nil {
			if err := e.recordPause(ctx, &s); err != nil {
				return refuse(err)
			}
		}
		if err := e.pauseMaintenanceWriters(ctx, &s); err != nil {
			return fail(err)
		}
	case "effect":
		if !p.Local.Primary || s.Phase != "paused" && s.Phase != "effect" {
			return fail(errors.New("the primary effect requires confirmed local writer pause"))
		}
		if s.Maintenance.Kind == "backup" && command.EffectAction != "backup" {
			return fail(errors.New("the effect differs from the frozen backup"))
		}
		if s.Maintenance.Kind == "upgrade" {
			status, err := e.Journal.upgrade(s.Maintenance.ResourceID)
			if err != nil && command.EffectAction != "prepare" || err == nil && !status.Status.Allows(command.EffectAction) {
				return fail(errors.New("the upgrade action is not allowed by its durable state"))
			}
		}
		s.Phase, s.EffectAction = "effect", command.EffectAction
		if err := e.Journal.saveCrossHost(s); err != nil {
			return err
		}
	case "resume-source", "resume-target":
		if s.Phase != "reserved" && s.Phase != "pausing" && s.Phase != "paused" && s.Phase != "effect" && s.Phase != "restored" && s.Phase != "resuming" && s.Phase != "resumed" {
			return fail(errors.New("writer resumption is out of phase"))
		}
		target := p
		if command.Phase == "resume-target" {
			if s.Maintenance.Target == nil {
				return fail(errors.New("no target was frozen for this maintenance"))
			}
			target = *s.Maintenance.Target
		}
		useTarget := command.Phase == "resume-target"
		if s.ResumeTarget != nil && *s.ResumeTarget != useTarget {
			return fail(errors.New("the chosen maintenance recovery target cannot change during resumption"))
		}
		if p.Local.Primary {
			if err := e.maintenanceOutcome(s, command.Phase == "resume-target"); err != nil {
				return fail(err)
			}
		}
		s.ResumeTarget = &useTarget
		if err := e.Journal.saveCrossHost(s); err != nil {
			return err
		}
		if err := e.resumeMaintenanceWriters(ctx, c, &s, target); err != nil {
			return fail(err)
		}
	case "complete":
		if s.Phase != "resumed" {
			return fail(errors.New("every recorded writer must be reconciled before releasing maintenance"))
		}
		current, err := e.Journal.unit(p.UnitID)
		if err != nil {
			return err
		}
		expected := p
		if command.UseTarget && s.Maintenance.Target != nil {
			expected = *s.Maintenance.Target
		}
		if s.ResumeTarget == nil || *s.ResumeTarget != command.UseTarget || current.Plan.Hash != expected.Hash {
			return fail(errors.New("maintenance completion differs from the confirmed recovery target"))
		}
		// A host whose reservation never reached its pause has changed nothing.
		if s.Pause != nil {
			if err = e.waitLocalHealth(ctx, s, expected); err != nil {
				return fail(err)
			}
			if _, err = e.inspectMaintenanceWriters(ctx, s, expected, current.Expected); err != nil {
				return fail(fmt.Errorf("the resumed unit changed before maintenance completion: %w", err))
			}
		}
		o, _, err := e.observeUnit(ctx, current.Plan, command.Secrets)
		if err != nil || s.Pause != nil && o.InfrastructureDrift {
			return fail(errors.New("the resumed unit changed before maintenance completion"))
		}
		waived, err := completionInventory(expected, o, s)
		if err != nil {
			return fail(fmt.Errorf("the resumed local inventory is not ready for maintenance completion: %w", err))
		}
		o.Applied = true
		note := "Writer resumption is confirmed; this host's maintenance reservation is released."
		if s.Pause == nil {
			note = "This host stopped nothing; its maintenance reservation is released."
		} else if waived > 0 {
			note = fmt.Sprintf("This host's maintenance reservation is released. %d instances were not required to be ready: they were already stopped or failing, or an operator accepted them.", waived)
		}
		// A restored volume may be served by a later release than the one that filled it. Other
		// maintenance changes no release; an unfinished rebuild waits for the next deployment.
		if s.Maintenance.Kind == "restore" && command.UseTarget {
			rebuild, err := e.reindexPostgres(ctx, expected, func() error { return e.checkpoint(c.Id, "postgres-reindex") })
			if err != nil {
				return err
			}
			// The owner's instructions come first, so a clipped note never loses them.
			note = strings.TrimSpace(rebuild + " " + note)
		}
		return e.finishCrossHost(c, command, o, note, true)
	default:
		return fail(errors.New("unsupported maintenance phase"))
	}
	current, err := e.Journal.unit(p.UnitID)
	if err != nil {
		return err
	}
	o, _, err := e.observeUnit(ctx, current.Plan, command.Secrets)
	if err != nil {
		return err
	}
	return e.finishCrossHost(c, command, o, "Coordinated maintenance phase acknowledged: "+command.Phase+".", false)
}

// maintenanceWriters works out what a pause would record, and refuses a source that changed
// in anything but whether its instances run.
func (e *Engine) maintenanceWriters(ctx context.Context, s crossHostState) (*writerPause, error) {
	p := s.Unit.Plan
	current, err := e.Journal.unit(p.UnitID)
	if err != nil || current.Plan.Hash != p.Hash {
		return nil, errors.New("maintenance source plan changed")
	}
	pause := &writerPause{Serving: maps.Clone(current.Expected), Writers: map[string]pausedWriter{}}
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != app.ID.String() {
				return nil, errors.New("maintenance writer ownership changed")
			}
			if app.Postgres == nil || !v.Container.State.Running {
				pause.Writers[id.String()] = pausedWriter{ContainerID: v.Container.ID, Running: v.Container.State.Running}
			}
		}
	}
	s.Pause = pause
	if _, err = e.inspectMaintenanceWriters(ctx, s, p, pause.Serving); err != nil {
		return nil, err
	}
	return pause, nil
}

func (e *Engine) recordPause(ctx context.Context, s *crossHostState) error {
	o, _, err := e.observeUnit(ctx, s.Unit.Plan, nil)
	if err != nil || o.InfrastructureDrift || o.StateHash != s.Maintenance.ExpectedState {
		return errors.New("maintenance source drift requires review")
	}
	s.Pause, err = e.maintenanceWriters(ctx, *s)
	if err != nil {
		return err
	}
	s.Phase = "pausing"
	if err = e.Journal.saveCrossHost(*s); err != nil {
		return err
	}
	e.point("maintenance-writers-recorded")
	return nil
}

func (e *Engine) pauseMaintenanceWriters(ctx context.Context, s *crossHostState) error {
	p := s.Unit.Plan
	if _, err := e.inspectMaintenanceWriters(ctx, *s, p, s.Pause.Serving); err != nil {
		return err
	}
	for _, writer := range s.Pause.Writers {
		if writer.Running {
			if _, err := e.Docker.ContainerStop(ctx, writer.ContainerID, client.ContainerStopOptions{}); err != nil {
				return err
			}
			e.point("maintenance-writer-stopped")
		}
	}
	expected, err := e.inspectMaintenanceWriters(ctx, *s, p, s.Pause.Serving)
	if err != nil {
		return err
	}
	for _, writer := range s.Pause.Writers {
		v, err := e.Docker.ContainerInspect(ctx, writer.ContainerID, client.ContainerInspectOptions{})
		if err != nil || v.Container.State.Running {
			return errors.New("a recorded maintenance writer remains running")
		}
	}
	if err = e.Journal.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		return err
	}
	s.Phase = "paused"
	return e.Journal.saveCrossHost(*s)
}

// Only the recorded Running transition is exempt from the serving fingerprint, which
// describes a running writer even for one found stopped. The exact container identity and
// all other configuration must still match.
func (e *Engine) inspectMaintenanceWriters(ctx context.Context, s crossHostState, p deployment.Plan, baseline map[string]string) (map[string]string, error) {
	expected := map[string]string{}
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != app.ID.String() {
				return nil, errors.New("maintenance resource identity changed")
			}
			key, running := id.String(), v.Container.State.Running
			want := baseline[key]
			if replacement, ok := maintenanceReplacement(s, p, key); ok {
				if replacement.ContainerID != v.Container.ID || !replacement.Running && running {
					return nil, errors.New("restored writer identity or running state changed")
				}
				running, want = replacement.Running, replacement.Fingerprint
			} else if writer, ok := s.Pause.Writers[key]; ok {
				if writer.ContainerID != v.Container.ID || !writer.Running && running {
					return nil, errors.New("maintenance writer identity or running state changed")
				}
				running, want = true, s.Pause.Serving[key]
			}
			if want == "" || visualFingerprint(v.Container, running) != want {
				return nil, errors.New("maintenance resource configuration drift requires review")
			}
			expected[key] = visualFingerprint(v.Container, v.Container.State.Running)
		}
	}
	if len(expected) != len(baseline) {
		return nil, errors.New("maintenance resource inventory changed")
	}
	return expected, nil
}

func (e *Engine) resumeMaintenanceWriters(ctx context.Context, c api.ExecutionCommand, s *crossHostState, target deployment.Plan) error {
	if s.Pause == nil {
		s.Phase = "resumed"
		return e.Journal.saveCrossHost(*s)
	}
	current, err := e.Journal.unit(target.UnitID)
	if err != nil || current.Plan.Hash != s.Unit.Plan.Hash && current.Plan.Hash != target.Hash {
		return errors.New("the maintenance target requires reconciliation")
	}
	if _, err = e.inspectMaintenanceWriters(ctx, *s, target, current.Expected); err != nil {
		return err
	}
	s.Phase = "resuming"
	if err = e.Journal.saveCrossHost(*s); err != nil {
		return err
	}
	// One writer that cannot start does not keep the others from serving, and leaves only
	// its own state for an operator to accept. One that was already failing needs no
	// acceptance: maintenance cannot have broken it, and it records below as left stopped.
	var started error
	for key, writer := range s.Pause.Writers {
		if replacement, ok := maintenanceReplacement(*s, target, key); ok {
			writer.ContainerID = replacement.ContainerID
		}
		if writer.Running {
			if _, err = e.Docker.ContainerStart(ctx, writer.ContainerID, client.ContainerStartOptions{}); err != nil {
				if !s.Unready[key] {
					started = errors.Join(started, err)
				}
				continue
			}
			e.point("maintenance-writer-resumed")
		}
	}
	if started != nil {
		return started
	}
	for _, app := range target.LocalApplications() {
		if app.Health == nil {
			continue
		}
		for _, id := range target.InstanceIDs(app.ID) {
			if writer, ok := s.Pause.Writers[id.String()]; ok && writer.Running && !s.Unready[id.String()] {
				if err = e.waitHTTPInstance(ctx, c, target, app, id); err != nil {
					return fmt.Errorf("resumed writer %s has not passed readiness: %w", id, err)
				}
			}
		}
	}
	expected, err := e.inspectMaintenanceWriters(ctx, *s, target, current.Expected)
	if err != nil {
		return err
	}
	for key, writer := range s.Pause.Writers {
		if replacement, ok := maintenanceReplacement(*s, target, key); ok {
			writer.ContainerID = replacement.ContainerID
		}
		v, err := e.Docker.ContainerInspect(ctx, writer.ContainerID, client.ContainerInspectOptions{})
		if err == nil && writer.Running && !v.Container.State.Running && s.Unready[key] {
			writer.Running = false
			s.Pause.Writers[key] = writer
		}
		if err != nil || v.Container.State.Running != writer.Running {
			return errors.New("recorded writer resumption is incomplete")
		}
		// Starting it by hand later is not drift, so it keeps describing the running writer.
		if !writer.Running {
			expected[key] = visualFingerprint(v.Container, true)
		}
	}
	if err = e.Journal.saveUnit(unitState{Plan: target, Expected: expected}); err != nil {
		return err
	}
	s.Phase = "resumed"
	return e.Journal.saveCrossHost(*s)
}

// waitLocalHealth waits at completion rather than in resumption, which every host must
// finish first: until the primary's has succeeded, other hosts' writers stay stopped. It
// covers every instance a restart of the host could also have left starting, except one
// that maintenance leaves stopped or that is not expected to be ready.
func (e *Engine) waitLocalHealth(ctx context.Context, s crossHostState, p deployment.Plan) error {
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			if !s.mustBeReady(id) {
				continue
			}
			if err := e.waitContainerHealthy(ctx, deployment.UnitName(id)); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitContainerHealthy waits for Docker's verdict on a container started directly, as
// compose up --wait does for one started that way. A health check restarts with its
// container, whether it comes from the plan or the image, and until its first probe the
// state reads starting, which completion refuses. An unhealthy container can still recover
// on its next probe, so only the limit, the same as compose's, ends the wait.
func (e *Engine) waitContainerHealthy(ctx context.Context, name string) error {
	status := container.Starting
	for range 60 {
		v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		health := v.Container.State.Health
		if health == nil || health.Status == container.Healthy {
			return nil
		}
		status = health.Status
		if !wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("the health check of %s is still %s after 60 seconds", name, status)
}

func (e *Engine) maintenanceOutcome(s crossHostState, target bool) error {
	if s.EffectAction == "" && !target {
		return nil
	}
	if s.Maintenance.Kind == "restore" && target && s.Restore != nil && s.Restore.Stage == "complete" {
		return nil
	}
	if s.Maintenance.Kind == "backup" && !target {
		state, err := e.Journal.schedule(s.Maintenance.ResourceID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil && len(state.Writers) == 0 {
			found := false
			for _, run := range state.Runs {
				if run.ID == s.Maintenance.RunID {
					found = true
					if run.State == "succeeded" || run.State == "failed" {
						return nil
					}
				}
			}
			if !found {
				return nil
			}
		}
	}
	if s.Maintenance.Kind == "upgrade" {
		state, err := e.Journal.upgrade(s.Maintenance.ResourceID)
		if errors.Is(err, os.ErrNotExist) && !target {
			return nil
		}
		if err == nil && (target && state.Status.Phase == "complete" || !target && (state.Status.Phase == "returned" || state.Status.Phase == "rejected")) {
			return nil
		}
	}
	return errors.New("the primary data effect has no confirmed result permitting writer resumption")
}
