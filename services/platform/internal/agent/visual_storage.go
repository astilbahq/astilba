package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// ensureStorage creates the unit's networks and data volumes, and checks that each existing volume
// is the unit's own and that a PostgreSQL service on it runs the major that initialised it. Every
// deployment path reaches it before starting a service.
func (e *Engine) ensureStorage(ctx context.Context, p deployment.Plan) error {
	if err := e.ensureUnitNetworks(ctx, p); err != nil {
		return err
	}
	if p.Local != nil && !p.Local.Primary {
		return nil
	}
	for _, vol := range p.Specification.Volumes {
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			_, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(vol.ID), Driver: "local", Labels: volumeLabels(p, vol.ID)})
			if err != nil {
				return errors.New("Cannot create a data volume.")
			}
			if err = e.Journal.recordNewPostgresStorage(p, vol.ID); err != nil {
				return err
			}
			v, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		}
		if err != nil || !visualOwned(v.Volume.Labels, p) || v.Volume.Labels[labelResource] != vol.ID.String() || v.Volume.Driver != "local" || len(v.Volume.Options) > 0 {
			return errors.New("Data volume ownership or driver does not match.")
		}
		if err = postgresVolumeMatches(v.Volume.Labels, p, vol.ID); err != nil {
			return err
		}
	}
	return nil
}
