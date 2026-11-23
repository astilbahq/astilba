package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// ensureStorage creates the unit's networks and data volumes, a managed database's with its
// cluster in the owner model, and checks that each existing volume is the unit's own and that a
// PostgreSQL service on it runs the major and the role model that initialised it. Every
// deployment path reaches it before starting a service. secrets are the plan's, which hold a new
// database user's password.
func (e *Engine) ensureStorage(ctx context.Context, p deployment.Plan, secrets map[string]string) error {
	if err := e.ensureUnitNetworks(ctx, p); err != nil {
		return err
	}
	if p.Local != nil && !p.Local.Primary {
		return nil
	}
	for _, vol := range p.Specification.Volumes {
		database, isDatabase := postgresOn(p, vol.ID)
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		if err == nil && isDatabase && visualOwned(v.Volume.Labels, p) && v.Volume.Labels[labelResource] == vol.ID.String() {
			// A volume whose cluster an earlier attempt didn't finish creating never served the
			// database, so it is made again.
			record, found, recordErr := e.Journal.postgresData(vol.ID)
			if recordErr != nil {
				return recordErr
			}
			if found && record.Initialising {
				if err = e.removeLeftoverInit(ctx, p, vol.ID); err != nil {
					return err
				}
				if _, err = e.Docker.VolumeRemove(ctx, deployment.VolumeName(vol.ID), client.VolumeRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
					return errors.New("Cannot remove a database volume whose creation didn't finish.")
				}
				v, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
			}
		}
		if errdefs.IsNotFound(err) {
			if isDatabase && !database.Postgres.Owner() {
				return errLegacyNewDatabase
			}
			if isDatabase {
				if err = e.Journal.recordNewPostgresStorage(p, vol.ID, true); err != nil {
					return err
				}
			}
			_, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(vol.ID), Driver: "local", Labels: volumeLabels(p, vol.ID)})
			if err != nil {
				return errors.New("Cannot create a data volume.")
			}
			if isDatabase {
				verifier, err := e.initOwnerCluster(ctx, p, database, secrets[database.Postgres.PasswordSecretVersionID.String()])
				if errors.Is(err, errUnsupportedPostgresImage) || errors.Is(err, errNoDatabasePassword) {
					return err
				}
				if err != nil {
					slog.Warn("creating a database's cluster failed", "volume", vol.ID, "error", err)
					return errors.New("Cannot create the database's cluster. Retrying the deployment creates it again.")
				}
				if err = e.Journal.savePostgresCredential(vol.ID, verifier); err != nil {
					return err
				}
				if err = e.Journal.recordNewPostgresStorage(p, vol.ID, false); err != nil {
					return err
				}
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
