package store

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// VolumeDeletion is how far a delete of one of an environment's volumes has got.
type VolumeDeletion int

const (
	// VolumeKept is a volume no delete has finished with or is still working on.
	VolumeKept VolumeDeletion = iota
	// VolumeDeleting is a volume a delete-volume operation that hasn't ended names. If it fails
	// or is cancelled, the volume is kept.
	VolumeDeleting
	// VolumeDeleted is a volume a delete removed, or one its environment gave up.
	VolumeDeleted
)

// LockVolumeDecisions takes env's row lock. A delete of one of env's volumes and every new draft
// or plan that uses one are decided under it, so neither can miss the other. The workspace write
// lock alone doesn't order them: a pipeline's report writes a draft without it. Under READ
// COMMITTED each statement after the lock sees what the other transaction committed while this
// one waited. Take it before locking any of env's volumes' rows.
func LockVolumeDecisions(ctx context.Context, q *db.Queries, ws, env uuid.UUID) error {
	_, err := q.LockEnvironmentDraft(ctx, db.LockEnvironmentDraftParams{WorkspaceID: ws, ID: env})
	return err
}

// LockedDraft takes LockVolumeDecisions' lock and returns env's draft as it stands once the lock
// is held.
func LockedDraft(ctx context.Context, q *db.Queries, ws, env uuid.UUID) (domain.Specification, error) {
	draft, err := q.LockEnvironmentDraft(ctx, db.LockEnvironmentDraftParams{WorkspaceID: ws, ID: env})
	if err != nil {
		return domain.Specification{}, err
	}
	rev, err := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: draft})
	if err != nil {
		return domain.Specification{}, err
	}
	spec, _, err := domain.ParseSpec(rev.Specification)
	return spec, err
}

// VolumeDeletions takes LockVolumeDecisions' lock and returns those of env's volumes among ids
// that are deleted or being deleted. A volume that isn't env's is left out.
func VolumeDeletions(ctx context.Context, q *db.Queries, ws, env uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]VolumeDeletion, error) {
	if err := LockVolumeDecisions(ctx, q, ws, env); err != nil {
		return nil, err
	}
	rows, err := q.VolumeDeletions(ctx, db.VolumeDeletionsParams{WorkspaceID: ws, EnvironmentID: env, Ids: ids})
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]VolumeDeletion{}
	for _, row := range rows {
		out[row.ID] = VolumeDeleting
		if row.Deleted {
			out[row.ID] = VolumeDeleted
		}
	}
	return out, nil
}

// RefuseDeletedVolumes refuses volumes, those a new draft or plan of env's uses, when one is
// deleted or being deleted. Once a delete has run, a plan that uses its volume would start
// without the volume's data, or not at all. names gives each volume's name for the refusal.
func RefuseDeletedVolumes(ctx context.Context, q *db.Queries, ws, env uuid.UUID, volumes []uuid.UUID, names map[uuid.UUID]string) error {
	if len(volumes) == 0 {
		return nil
	}
	deletions, err := VolumeDeletions(ctx, q, ws, env, volumes)
	if err != nil {
		return err
	}
	for _, id := range volumes {
		if state, found := deletions[id]; found {
			return domain.UsesDeletedVolume(names[id], id, state == VolumeDeleting)
		}
	}
	return nil
}

// RefuseDeletedMounts refuses spec, a new draft of env's, when it mounts a volume that is deleted
// or being deleted. A draft that only still declares such a volume is kept, so an owner can remove
// it from their source files after the delete. Applying a plan refuses it until they do.
func RefuseDeletedMounts(ctx context.Context, q *db.Queries, ws, env uuid.UUID, spec domain.Specification) error {
	return RefuseDeletedVolumes(ctx, q, ws, env, domain.MountedVolumes(spec), domain.VolumeNames(spec))
}
