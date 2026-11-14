package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func (e *Engine) sourceContainer(ctx context.Context, p backup.Policy, id uuid.UUID) (container.InspectResponse, error) {
	list, err := e.ownedContainers(ctx, p.Plan)
	if err != nil {
		return container.InspectResponse{}, err
	}
	for _, v := range list {
		if v.Labels[labelResource] == id.String() {
			found, err := e.Docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
			if err != nil {
				return container.InspectResponse{}, err
			}
			if !visualOwned(found.Container.Config.Labels, p.Plan) || found.Container.Config.Labels[labelResource] != id.String() || found.Container.Config.Image != p.Plan.Images[id.String()] {
				return container.InspectResponse{}, errors.New("source ownership or image changed")
			}
			return found.Container, nil
		}
	}
	return container.InspectResponse{}, errors.New("source not found")
}
func sourceApp(p backup.Policy) (domain.Application, error) {
	for _, a := range p.Plan.Specification.Applications {
		if a.ID == p.ResourceID {
			return a, nil
		}
	}
	return domain.Application{}, errors.New("source not found")
}
func (e *Engine) checkBackupSource(ctx context.Context, p backup.Policy) error {
	if e.Docker == nil {
		return errors.New("Docker unavailable")
	}
	cfg, err := e.Journal.Config()
	if err != nil || cfg.ServerID != p.ServerID || cfg.Role == "build" {
		return errors.New("source server mismatch")
	}
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil || unit.Plan.Hash != p.Plan.Hash {
		return errors.New("policy must be refreshed after deployment")
	}
	if p.Method == "container-job" && elevatedService(p.Plan, p.ResourceID) {
		return errElevatedJob
	}
	observation, _, err := e.observeUnit(ctx, p.Plan, nil)
	for _, resource := range observation.Resources {
		observation.ResourceDrift = observation.ResourceDrift || resource.Drift
	}
	if err != nil || observation.ResourceDrift || observation.InfrastructureDrift {
		return errors.New("source drift requires review")
	}
	if p.Method == "postgres-logical" || p.Method == "container-job" {
		a, err := sourceApp(p)
		if err != nil {
			return err
		}
		if p.Method == "postgres-logical" && (a.Postgres == nil || p.Plan.Specification.Compose != nil || !deployment.ResolvedImage(*a.Source.Image, p.Plan.Images[a.ID.String()])) {
			return errors.New("unsupported PostgreSQL recipe")
		}
		if p.Method == "container-job" && (a.Postgres != nil || p.Plan.Specification.Compose != nil) {
			return errors.New("scheduled jobs require an image worker")
		}
		c, err := e.sourceContainer(ctx, p, p.ResourceID)
		if err != nil {
			return err
		}
		if !c.State.Running {
			return errors.New("source is stopped")
		}
		return nil
	}
	exists := false
	for _, v := range p.Plan.Specification.Volumes {
		exists = exists || v.ID == p.ResourceID
	}
	if !exists {
		return errors.New("volume missing")
	}
	for _, a := range p.Plan.Specification.Applications {
		for _, m := range a.Mounts {
			if m.VolumeID == p.ResourceID && a.Postgres != nil {
				return errors.New("database files require native logical backup")
			}
		}
	}
	_, err = e.volumePath(ctx, p, p.ResourceID)
	return err
}
func (e *Engine) volumePath(ctx context.Context, p backup.Policy, id uuid.UUID) (string, error) {
	v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(id), client.VolumeInspectOptions{})
	if err != nil || !visualOwned(v.Volume.Labels, p.Plan) || v.Volume.Labels[labelResource] != id.String() || v.Volume.Driver != "local" || len(v.Volume.Options) > 0 {
		return "", errors.New("volume ownership or driver mismatch")
	}
	return v.Volume.Mountpoint, nil
}
