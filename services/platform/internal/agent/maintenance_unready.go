package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// recordUnready notes, before any host stops anything, which running instances are already
// failing. Maintenance cannot have caused that and cannot cure it, so neither resumption nor
// completion waits for them. That keeps a failing application from blocking the backup or
// restore that may be its remedy, or from keeping other hosts' writers stopped. Probing any
// later could blame a writer for a dependency another host has paused.
func (e *Engine) recordUnready(ctx context.Context, c api.ExecutionCommand, s *crossHostState) error {
	p := s.Unit.Plan
	s.Unready = map[string]bool{}
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			if !v.Container.State.Running {
				continue
			}
			if health := v.Container.State.Health; health != nil && health.Status == container.Unhealthy {
				s.Unready[id.String()] = true
			} else if app.Health != nil {
				err := e.waitHTTPInstance(ctx, c, p, app, id)
				if err != nil && !errors.Is(err, errNotReady) {
					return err
				}
				if err != nil {
					s.Unready[id.String()] = true
				}
			}
		}
	}
	return e.Journal.saveCrossHost(*s)
}

// mustBeReady reports whether completion requires an instance to be running and ready.
func (s crossHostState) mustBeReady(id uuid.UUID) bool {
	if s.Pause == nil || s.Unready[id.String()] {
		return false
	}
	writer, recorded := s.Pause.Writers[id.String()]
	return !recorded || writer.Running
}

// completionInventory checks an observation against what completion requires, and counts the
// instances excused from being ready, whatever they read as now. This is where readiness is
// enforced: see CrossHostCommand.ValidateCompletion.
func completionInventory(p deployment.Plan, o deployment.Observation, s crossHostState) (int, error) {
	if err := p.ValidateMaintenanceObservation(o); err != nil {
		return 0, err
	}
	waived := 0
	for _, res := range o.Resources {
		for _, instance := range res.Instances {
			if !s.mustBeReady(instance.InstanceID) {
				waived++
			} else if instance.State != "running" && instance.State != "healthy" {
				return 0, fmt.Errorf("instance %s of resource %s is %s", instance.InstanceID, res.ID, instance.State)
			}
		}
	}
	return waived, nil
}

// AcceptUnreadyWriters records an operator's decision, after inspecting the host, that its
// writers need not become ready for this maintenance to finish. It is the way out when a
// writer does not come back and the fix needs the reservation released, such as deploying a
// corrected revision. Every recorded writer stays as it is now: one that could not start
// stays stopped. It never touches a container, excuses changed configuration or a database,
// or decides a data effect's outcome, and the resumption or completion command still has to
// pass everything else when the agent next runs it.
func (e *Engine) AcceptUnreadyWriters(ctx context.Context, id uuid.UUID) error {
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
	command, err := crossHostCommand(r.Command)
	resuming := err == nil && (command.Phase == "resume-source" || command.Phase == "resume-target") && s.Phase == "resuming"
	completing := err == nil && command.Phase == "complete" && s.Phase == "resumed"
	if s.Maintenance == nil || s.Pause == nil || !resuming && !completing {
		return errors.New("only an interrupted writer resumption or maintenance completion can be accepted")
	}
	target := s.Unit.Plan
	if command.Phase == "resume-target" || command.UseTarget {
		if s.Maintenance.Target == nil {
			return errors.New("no target was frozen for this maintenance")
		}
		target = *s.Maintenance.Target
	}
	current, err := e.Journal.unit(target.UnitID)
	if err != nil {
		return err
	}
	if _, err = e.inspectMaintenanceWriters(ctx, s, target, current.Expected); err != nil {
		return fmt.Errorf("this host's writers changed, which accepting them cannot excuse: %w", err)
	}
	if s.Unready == nil {
		s.Unready = map[string]bool{}
	}
	for key, writer := range s.Pause.Writers {
		if !writer.Running {
			continue
		}
		if replacement, ok := maintenanceReplacement(s, target, key); ok {
			writer.ContainerID = replacement.ContainerID
		}
		v, err := e.Docker.ContainerInspect(ctx, writer.ContainerID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		s.Unready[key] = true
		if !v.Container.State.Running {
			writer.Running = false
			s.Pause.Writers[key] = writer
		}
	}
	return e.Journal.db.Update(func(tx *bolt.Tx) error {
		if err := e.Journal.checkController(tx, r.Command); err != nil {
			return err
		}
		if err := e.Journal.crossHostAdmission(tx, r.Command); err != nil {
			return err
		}
		if err := e.Journal.put(tx, "cross-host", "active", s); err != nil {
			return err
		}
		r.Report.State = "running"
		r.Report.Note = "Local operator accepted this host's writers as they are after inspection. No container was changed."
		r.Report.Sequence++
		return e.Journal.put(tx, "operations", id.String(), r)
	})
}
