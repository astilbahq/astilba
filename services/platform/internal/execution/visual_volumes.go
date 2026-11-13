package execution

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// claimPlanVolumes records each volume an applied plan mounts as the environment's, and refuses
// a PostgreSQL service that the volume was not initialised for: other credentials, or another
// major version, which the volume keeps recording across retirement and restore.
func claimPlanVolumes(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, plan deployment.Plan) error {
	for _, vol := range plan.Specification.Volumes {
		var owner uuid.UUID
		var deleted bool
		var initial, stored, major, storedMajor *string
		for _, app := range plan.Specification.Applications {
			if app.Postgres != nil && app.Mounts[0].VolumeID == vol.ID {
				value := deployment.Hash(app.Postgres)
				initial = &value
				if version := domain.PostgresMajor(*app.Source.Image); version != "" {
					major = &version
				}
			}
		}
		err := tx.QueryRow(ctx, `INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_init_hash,postgres_major) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,id) DO UPDATE SET id=EXCLUDED.id RETURNING environment_id,deleted,postgres_init_hash,postgres_major`, ws, env, plan.UnitID, vol.ID, initial, major).Scan(&owner, &deleted, &stored, &storedMajor)
		if err != nil {
			return err
		}
		if owner != env || deleted {
			return domain.Conflict
		}
		if initial != nil {
			if stored != nil && *stored != *initial {
				return domain.Invalid("applications.postgres.existingVolume")
			}
			if storedMajor != nil && (major == nil || *storedMajor != *major) {
				return domain.Invalid("applications.postgres.upgrade")
			}
			if stored == nil || storedMajor == nil && major != nil {
				if _, err := tx.Exec(ctx, "UPDATE managed_volumes SET postgres_init_hash=$3,postgres_major=$4 WHERE workspace_id=$1 AND id=$2", ws, vol.ID, initial, major); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
