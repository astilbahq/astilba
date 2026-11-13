package execution

import (
	"context"
	"encoding/json/v2"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// unknownRestoreImage reports a run that names a restore release Astilba never pinned.
func unknownRestoreImage(r backup.Run) bool {
	_, ok := domain.PostgresReleaseOf(r.RestoreImage)
	return r.RestoreImage != "" && !ok
}

// restoredDatabaseOpens refuses a cutover whose restored data the draft's release cannot open,
// such as data PostgreSQL 17 wrote under a PostgreSQL 18 database.
func restoredDatabaseOpens(spec domain.Specification, p backup.Policy, run backup.Run) error {
	for _, a := range spec.Applications {
		if a.ID == p.ResourceID {
			if err := backup.OpensRestoredPostgres(a, run); err != nil {
				return restoreIncompatible(err)
			}
			return nil
		}
	}
	return domain.Invalid("restore.compatibility")
}

// restoreIncompatible names why a restored target cannot replace the database.
func restoreIncompatible(err error) error {
	return &domain.Error{Code: "invalid_input", Message: "This restore cannot replace the database: " + err.Error() + ".", Fields: []string{"restore.compatibility"}}
}

// mountsVolume reports whether the specification's managed database keeps its data on the volume.
func mountsVolume(spec domain.Specification, resource, volume uuid.UUID) bool {
	for _, a := range spec.Applications {
		if a.ID == resource {
			return len(a.Mounts) == 1 && a.Mounts[0].VolumeID == volume
		}
	}
	return false
}

// checkRestoreRequest refuses a restore of an unknown snapshot, and, before anything runs, one
// whose archive the database's own release cannot restore, such as a PostgreSQL 17 backup of a
// database now on PostgreSQL 18. The agent checks again; this names the reason before an
// operation is queued, including for agents that predate the check.
func checkRestoreRequest(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, p backup.Policy, snapshot string) error {
	if !backup.SnapshotID(snapshot) {
		return domain.Invalid("snapshot")
	}
	if p.Method != "postgres-logical" {
		return nil
	}
	raw, err := db.New(tx).BackupPolicyStatus(ctx, db.BackupPolicyStatusParams{WorkspaceID: ws, EnvironmentID: env, ID: p.ID})
	if err != nil {
		return err
	}
	var status backup.Status
	if json.Unmarshal(raw, &status) != nil {
		return domain.Conflict
	}
	for _, r := range status.Runs {
		if r.Snapshot != snapshot || r.State != "succeeded" || r.Manifest == nil {
			continue
		}
		if _, err := backup.RestoreRelease(p, *r.Manifest); err != nil {
			return &domain.Error{Code: "invalid_input", Message: "Restore refused: " + err.Error() + ".", Fields: []string{"snapshot"}}
		}
		return nil
	}
	return nil
}
