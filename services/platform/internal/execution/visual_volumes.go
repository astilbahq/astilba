package execution

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// refusePlanDeletedVolumes refuses a plan of spec when it declares a volume that is deleted or
// being deleted, mounted or not. A draft may still declare one after a delete, until its owner
// removes it from their source files, but no plan of it is prepared or applied until then.
func refusePlanDeletedVolumes(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, spec domain.Specification) error {
	ids := make([]uuid.UUID, 0, len(spec.Volumes))
	for _, vol := range spec.Volumes {
		ids = append(ids, vol.ID)
	}
	return store.RefuseDeletedVolumes(ctx, db.New(tx), ws, env, ids, domain.VolumeNames(spec))
}

// claimPlanVolumes records each volume an applied plan mounts as the environment's, and refuses
// one that is deleted or being deleted, or a PostgreSQL service that the volume was not
// initialised for: other credentials, or another major version, which the volume keeps recording
// across retirement and restore.
func claimPlanVolumes(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, plan deployment.Plan) error {
	if err := refusePlanDeletedVolumes(ctx, tx, ws, env, plan.Specification); err != nil {
		return err
	}
	for _, vol := range plan.Specification.Volumes {
		var owner uuid.UUID
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
		err := tx.QueryRow(ctx, `INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_init_hash,postgres_major) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,id) DO UPDATE SET id=EXCLUDED.id RETURNING environment_id,postgres_init_hash,postgres_major`, ws, env, plan.UnitID, vol.ID, initial, major).Scan(&owner, &stored, &storedMajor)
		if err != nil {
			return err
		}
		if owner != env {
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
