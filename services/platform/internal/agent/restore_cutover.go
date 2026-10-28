package agent

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

type restoredContainer struct {
	ContainerID string
	Fingerprint string
	Running     bool
}
type restoreCutoverState struct {
	Stage          string
	WritesPossible bool
	Original       map[string]pausedWriter
	Replacements   map[string]restoredContainer
}

func maintenanceReplacement(s crossHostState, p deployment.Plan, key string) (restoredContainer, bool) {
	if s.Restore == nil || s.Maintenance.Target == nil || p.Hash != s.Maintenance.Target.Hash {
		return restoredContainer{}, false
	}
	r, ok := s.Restore.Replacements[key]
	return r, ok
}

func (e *Engine) validateLocalRestore(s crossHostState) (backup.Policy, error) {
	r := s.Maintenance.Restore
	target := s.Maintenance.Target
	if r == nil || target == nil {
		return backup.Policy{}, errors.New("the restore target is not frozen")
	}
	expected, err := deployment.RestoreSpecification(s.Unit.Plan.Specification, r.OldVolumeID, r.TargetVolumeID)
	if err != nil || deployment.Hash(expected) != deployment.Hash(target.Specification) || deployment.Hash(s.Unit.Plan.Images) != deployment.Hash(target.Images) || deployment.Hash(s.Unit.Plan.Local.Instances) != deployment.Hash(target.Local.Instances) || deployment.Hash(s.Unit.Plan.Local.Requests) != deployment.Hash(target.Local.Requests) || deployment.Hash(s.Unit.Plan.Local.Outputs) != deployment.Hash(target.Local.Outputs) || deployment.Hash(s.Unit.Plan.Local.Backends) != deployment.Hash(target.Local.Backends) || target.Release != nil || target.ReleaseOptions != nil {
		return backup.Policy{}, errors.New("the restore target changes more than the approved volume")
	}
	state, err := e.Journal.schedule(r.PolicyID)
	if err != nil || state.Installation.Policy.Plan.Hash != s.Unit.Plan.Hash {
		return backup.Policy{}, errors.New("the restore policy no longer matches the source")
	}
	for _, run := range state.Runs {
		if run.ID != r.RunID {
			continue
		}
		old, err := backup.CutoverVolume(state.Installation.Policy, run)
		if err == nil && old == r.OldVolumeID && run.TargetID == r.TargetVolumeID && run.ManifestHash == r.ManifestHash {
			return state.Installation.Policy, nil
		}
		return backup.Policy{}, errors.New("the restored run is incompatible with the frozen target")
	}
	return backup.Policy{}, errors.New("the verified restored run is unavailable")
}

func restoreApplications(p deployment.Plan, old uuid.UUID) []domain.Application {
	out := []domain.Application{}
	for _, app := range p.LocalApplications() {
		for _, mount := range app.Mounts {
			if mount.VolumeID == old {
				out = append(out, app)
				break
			}
		}
	}
	return out
}

func (e *Engine) inspectRestoreVolumeUsers(ctx context.Context, s crossHostState, apps []domain.Application) error {
	r := s.Maintenance.Restore
	containers, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers.Items {
		for _, mount := range c.Mounts {
			if mount.Name != deployment.VolumeName(r.OldVolumeID) && mount.Name != deployment.VolumeName(r.TargetVolumeID) {
				continue
			}
			allowed := false
			for _, app := range apps {
				for _, id := range s.Unit.Plan.InstanceIDs(app.ID) {
					if slices.Contains(c.Names, "/"+deployment.UnitName(id)) && visualOwned(c.Labels, s.Unit.Plan) && c.Labels[labelResource] == app.ID.String() {
						allowed = true
					}
				}
			}
			if !allowed {
				return errors.New("an unapproved container consumes a restore volume")
			}
		}
	}
	return nil
}

func (e *Engine) activateRestoreCutover(ctx context.Context, c api.ExecutionCommand, secrets map[string]string, s *crossHostState) error {
	p, err := e.validateLocalRestore(*s)
	if err != nil {
		return err
	}
	r, target := s.Maintenance.Restore, *s.Maintenance.Target
	apps := restoreApplications(s.Unit.Plan, r.OldVolumeID)
	if len(apps) == 0 || s.Pause == nil {
		return errors.New("the restore has no frozen primary consumers")
	}
	if _, err = e.volumePath(ctx, p, r.OldVolumeID); err != nil {
		return err
	}
	if _, err = e.volumePath(ctx, p, r.TargetVolumeID); err != nil {
		return err
	}
	if err = e.inspectRestoreVolumeUsers(ctx, *s, apps); err != nil {
		return err
	}
	if s.Restore != nil && s.Restore.Stage == "complete" {
		return nil
	}
	if s.Restore == nil {
		current, err := e.Journal.unit(p.Plan.UnitID)
		if err != nil {
			return err
		}
		if _, err = e.inspectMaintenanceWriters(ctx, *s, p.Plan, current.Expected); err != nil {
			return err
		}
		state := &restoreCutoverState{Stage: "stopping", Original: map[string]pausedWriter{}, Replacements: map[string]restoredContainer{}}
		for _, app := range apps {
			for _, id := range p.Plan.InstanceIDs(app.ID) {
				v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
				if err != nil || !visualOwned(v.Container.Config.Labels, p.Plan) || v.Container.Config.Labels[labelResource] != app.ID.String() {
					return errors.New("a restore consumer changed ownership")
				}
				running := v.Container.State.Running
				if writer, ok := s.Pause.Writers[id.String()]; ok {
					running = writer.Running
				}
				state.Original[id.String()] = pausedWriter{ContainerID: v.Container.ID, Running: running}
			}
		}
		s.Restore, s.Phase, s.EffectAction = state, "restoring", "restore"
		if err = e.Journal.saveCrossHost(*s); err != nil {
			return err
		}
		e.point("restore-cutover-intent")
	}
	if s.Restore.Stage == "stopping" {
		current, err := e.Journal.unit(p.Plan.UnitID)
		if err != nil {
			return err
		}
		inspection := *s
		pause := *s.Pause
		pause.Writers = maps.Clone(pause.Writers)
		maps.Copy(pause.Writers, s.Restore.Original)
		inspection.Pause = &pause
		if _, err = e.inspectMaintenanceWriters(ctx, inspection, p.Plan, current.Expected); err != nil {
			return err
		}
		for _, original := range s.Restore.Original {
			if _, err = e.Docker.ContainerStop(ctx, original.ContainerID, client.ContainerStopOptions{}); err != nil {
				return err
			}
		}
		s.Restore.Stage = "creating"
		if err = e.Journal.saveCrossHost(*s); err != nil {
			return err
		}
	}
	if s.Restore.Stage == "creating" {
		if err = e.requireOwnedComposeServices(ctx, target, apps); err != nil {
			return err
		}
		body, err := deployment.Compose(target, secrets, false)
		if err != nil {
			return err
		}
		for _, app := range apps {
			if err = e.compose(ctx, target, body, secrets, "up", "--force-recreate", "--no-start", "--no-deps", deployment.ServiceName(app.ID)); err != nil {
				return err
			}
		}
		e.point("restore-cutover-created")
		for _, app := range apps {
			for _, id := range target.InstanceIDs(app.ID) {
				v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
				if err != nil || !visualOwned(v.Container.Config.Labels, target) || v.Container.Config.Labels[labelResource] != app.ID.String() || v.Container.State.Running || v.Container.Config.Image != target.Images[app.ID.String()] {
					return errors.New("restored consumer creation is unconfirmed")
				}
				mounted := false
				for _, mount := range v.Container.Mounts {
					mounted = mounted || mount.Name == deployment.VolumeName(r.TargetVolumeID)
					if mount.Name == deployment.VolumeName(r.OldVolumeID) {
						return errors.New("a restored consumer still mounts source data")
					}
				}
				if !mounted {
					return errors.New("the restored volume is not mounted")
				}
				original := s.Restore.Original[id.String()]
				s.Restore.Replacements[id.String()] = restoredContainer{ContainerID: v.Container.ID, Running: original.Running, Fingerprint: visualFingerprint(v.Container, original.Running)}
			}
		}
		s.Restore.Stage = "activating"
		if err = e.Journal.saveCrossHost(*s); err != nil {
			return err
		}
	}
	if s.Restore.Stage != "activating" {
		return errors.New("the recorded restore stage requires inspection")
	}
	current, err := e.Journal.unit(target.UnitID)
	if err != nil {
		return err
	}
	if _, err = e.inspectMaintenanceWriters(ctx, *s, target, current.Expected); err != nil {
		return err
	}
	// Private database clients can write before application writers resume.
	s.Restore.WritesPossible = true
	if err = e.Journal.saveCrossHost(*s); err != nil {
		return err
	}
	for _, app := range apps {
		if app.Postgres == nil {
			continue
		}
		for _, id := range target.InstanceIDs(app.ID) {
			replacement := s.Restore.Replacements[id.String()]
			if replacement.Running {
				if _, err = e.Docker.ContainerStart(ctx, replacement.ContainerID, client.ContainerStartOptions{}); err != nil {
					return err
				}
				if err = e.waitPostgresStartup(ctx, replacement.ContainerID, app.Postgres.Username, app.Postgres.Database); err != nil {
					return err
				}
			}
		}
	}
	e.point("restore-cutover-database")
	expected, err := e.inspectMaintenanceWriters(ctx, *s, target, current.Expected)
	if err != nil {
		return err
	}
	if err = e.Journal.saveUnit(unitState{Plan: target, Expected: expected}); err != nil {
		return err
	}
	s.Restore.Stage, s.Phase = "complete", "restored"
	return e.Journal.saveCrossHost(*s)
}
