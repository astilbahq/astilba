package deployment

import (
	"errors"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

type ReleaseOptions struct {
	Command               []string `json:"command"`
	CommandTimeoutSeconds int      `json:"commandTimeoutSeconds"`
	DrainSeconds          int      `json:"drainSeconds"`
}
type Release struct {
	ApplicationID  uuid.UUID      `json:"applicationId"`
	PreviousPlanID uuid.UUID      `json:"previousPlanId"`
	Options        ReleaseOptions `json:"options"`
}

func DefaultReleaseOptions() ReleaseOptions {
	return ReleaseOptions{Command: []string{}, CommandTimeoutSeconds: 60, DrainSeconds: 10}
}
func (o ReleaseOptions) Validate() error {
	if o.CommandTimeoutSeconds < 1 || o.CommandTimeoutSeconds > 300 || o.DrainSeconds < 1 || o.DrainSeconds > 120 || len(o.Command) > 32 {
		return errors.New("invalid release budgets")
	}
	for _, arg := range o.Command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return errors.New("invalid release command")
		}
	}
	if len(o.Command) > 0 && o.Command[0] == "" {
		return errors.New("empty release executable")
	}
	return nil
}
func (p Plan) Instance(id uuid.UUID) uuid.UUID {
	if p.Local != nil {
		if ids := p.Local.Instances[id.String()]; len(ids) > 0 {
			return ids[0]
		}
	}
	if v := p.Instances[id.String()]; v != uuid.Nil {
		return v
	}
	return id
}
func (p Plan) ContainerName(id uuid.UUID) string { return UnitName(p.Instance(id)) }
func (p Plan) HostName(id uuid.UUID) string      { return ServiceName(p.Instance(id)) }
func (p Plan) Backend(id uuid.UUID) Backend {
	if p.Local != nil && p.Local.Primary {
		if endpoints := p.Local.Backends[id.String()]; len(endpoints) > 0 {
			for _, a := range p.Specification.Applications {
				if a.ID == id {
					return Backend{Endpoints: endpoints, Health: a.Health}
				}
			}
		}
	}
	if p.Recipe == ReplicaRecipe {
		b := Backend{InstanceIDs: p.InstanceIDs(id)}
		for _, app := range p.Specification.Applications {
			if app.ID == id && app.ReplicaCount() > 1 {
				b.Health = app.Health
			}
		}
		return b
	}
	b := Backend{Address: p.HostName(id)}
	if v := p.Instances[id.String()]; v != uuid.Nil {
		b.InstanceID = &v
	}
	return b
}

// ReleaseApplication compares resolved effective plans, including moved mutable tags.
// Shared configuration and every other service must be unchanged.
func ReleaseApplication(old, next Plan) (domain.Application, error) {
	fail := errors.New("safe releases require exactly one changed stateless HTTP application and unchanged shared configuration")
	if old.ID == uuid.Nil || old.ServerID != next.ServerID || old.UnitID != next.UnitID || old.EnvironmentID != next.EnvironmentID || old.Specification.Compose != nil || next.Specification.Compose != nil || next.HostChanges || next.AcceptDrift {
		return domain.Application{}, fail
	}
	a, b := old.Specification, next.Specification
	a.Applications = nil
	b.Applications = nil
	if Hash(a) != Hash(b) || len(old.Specification.Applications) != len(next.Specification.Applications) {
		return domain.Application{}, fail
	}
	var changed domain.Application
	prior := map[uuid.UUID]domain.Application{}
	for _, app := range old.Specification.Applications {
		prior[app.ID] = app
	}
	for _, app := range next.Specification.Applications {
		before, ok := prior[app.ID]
		if !ok {
			return domain.Application{}, fail
		}
		if Hash(before) == Hash(app) && old.Images[app.ID.String()] == next.Images[app.ID.String()] && Hash(old.Artifacts[app.ID.String()]) == Hash(next.Artifacts[app.ID.String()]) {
			continue
		}
		if changed.ID != uuid.Nil || app.ServiceKind() != "web" || before.ServiceKind() != "web" || app.Health == nil || before.Health == nil || len(app.Mounts) > 0 || len(before.Mounts) > 0 || app.Postgres != nil || before.Postgres != nil {
			return domain.Application{}, fail
		}
		changed = app
	}
	if changed.ID == uuid.Nil {
		return changed, fail
	}
	routed := false
	for _, route := range next.Specification.Routes {
		if route.TargetID == changed.ID {
			if route.Protocol != "http" {
				return domain.Application{}, fail
			}
			routed = true
		}
	}
	if !routed {
		return domain.Application{}, fail
	}
	for _, spec := range []domain.Specification{old.Specification, next.Specification} {
		for _, binding := range spec.Bindings {
			if binding.SourceID == changed.ID {
				return domain.Application{}, fail
			}
		}
		for _, app := range spec.Applications {
			for _, dep := range domain.Dependencies(spec, app) {
				if dep == changed.ID {
					return domain.Application{}, fail
				}
			}
		}
	}
	return changed, nil
}
func (p Plan) validateInstances() error {
	apps := map[string]domain.Application{}
	used := map[uuid.UUID]bool{p.UnitID: true}
	for _, a := range p.Specification.Applications {
		apps[a.ID.String()] = a
		used[a.ID] = true
	}
	for _, v := range p.Specification.Volumes {
		used[v.ID] = true
	}
	for _, r := range p.Specification.Routes {
		used[r.ID] = true
	}
	if (p.Specification.HasReplicaConfiguration() || len(p.ReplicaInstances) > 0) && p.Recipe != ReplicaRecipe || p.Recipe == ReplicaRecipe && p.Specification.Compose != nil {
		return errors.New("replicas require a supported visual recipe")
	}
	if p.Specification.Compose != nil && (len(p.Instances) > 0 || p.Release != nil || p.ReleaseOptions != nil) {
		return errors.New("Compose cannot use visual release instances")
	}
	for id, instance := range p.Instances {
		a, ok := apps[id]
		if !ok || instance == uuid.Nil || used[instance] || a.Postgres != nil {
			return errors.New("invalid runtime instance")
		}
		used[instance] = true
	}
	for id, instances := range p.ReplicaInstances {
		if _, ok := apps[id]; !ok || len(instances) == 0 {
			return errors.New("invalid replica application")
		}
		for _, instance := range instances {
			if instance == uuid.Nil || used[instance] {
				return errors.New("invalid replica identity")
			}
			used[instance] = true
		}
	}
	for id, app := range apps {
		if len(p.ReplicaInstances[id]) != app.ReplicaCount()-1 {
			return errors.New("replica inventory differs from desired count")
		}
	}
	if p.ReleaseOptions != nil {
		if err := p.ReleaseOptions.Validate(); err != nil {
			return err
		}
	}
	if p.Release != nil {
		a, ok := apps[p.Release.ApplicationID.String()]
		if !ok || p.Release.PreviousPlanID == uuid.Nil || p.Release.PreviousPlanID == p.ID || p.Instances[a.ID.String()] == uuid.Nil || a.Health == nil || a.ServiceKind() != "web" || len(a.Mounts) > 0 || p.HostChanges || p.AcceptDrift {
			return errors.New("invalid protected release")
		}
		return p.Release.Options.Validate()
	}
	return nil
}
