package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PrepareBackupCutover writes a draft that switches a verified restore's target in for its source
// volume. Deploying that draft, through the ordinary plan review, is what performs the cutover.
func (s *Service) PrepareBackupCutover(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupCutoverRequest) (db.SpecificationRevision, error) {
	var out db.SpecificationRevision
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "backup.cutover.prepare", id, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if err = requireActive(v.State); err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		policy, err := loadBackupPolicy(ctx, tx, ws, env, id)
		if err != nil {
			return err
		}
		raw, err := db.New(tx).BackupPolicyStatus(ctx, db.BackupPolicyStatusParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return err
		}
		var status backup.Status
		if json.Unmarshal(raw, &status) != nil {
			return domain.Conflict
		}
		var run backup.Run
		for _, r := range status.Runs {
			if r.ID == in.RunId {
				run = r
			}
		}
		if !run.Verified || run.TargetID == uuid.Nil || run.State != "succeeded" || run.Manifest == nil || v.Applied == nil || policy.Plan.RevisionID != *v.Applied {
			return domain.Conflict
		}
		var current deployment.Plan
		if policy.Plan.Local != nil {
			if current, err = appliedVisualPlan(ctx, tx, ws, env); err != nil {
				return err
			}
			if current.Distributed == nil || current.PrimaryPlan().Hash != policy.Plan.Hash || deployment.Hash(v.Spec) != deployment.Hash(current.Specification) {
				return domain.Conflict
			}
		}
		source, err := backup.CutoverVolume(policy, run)
		if err != nil {
			return restoreIncompatible(err)
		}
		var boundary *deployment.RestoreCutover
		if policy.Plan.Local != nil {
			boundary = &deployment.RestoreCutover{SourcePlanID: current.ID, SourceHash: current.Hash, PolicyID: policy.ID, RunID: run.ID, ManifestHash: run.ManifestHash, OldVolumeID: source, TargetVolumeID: run.TargetID, Roles: run.RestoreRoles}
		}
		out, err = writeRestoreDraft(ctx, tx, p, ws, env, v, policy, run.ID, run.TargetID, source, run.RestoredImage(), run.RestoreRoles, boundary)
		return err
	})
	return out, err
}

// writeRestoreDraft writes a draft from v's that switches target, holding a verified restore of
// policy's resource from run, in for source, the volume it replaces, and records boundary with it
// for a distributed deployment. A logical restore's data was written by the restored image, in a
// cluster of the restored role model, which the draft's database takes. The target becomes the
// environment's own volume for the unit the policy's plan names, recorded with the database it
// holds; a deleted or another environment's target is refused.
func writeRestoreDraft(ctx context.Context, tx pgx.Tx, p store.Principal, ws, env uuid.UUID, v visualEnvironment, policy backup.Policy, run, target, source uuid.UUID, restored, roles string, boundary *deployment.RestoreCutover) (db.SpecificationRevision, error) {
	// The release that restored the target wrote its data, so the draft that will run on the
	// target, as the source the policy froze, must open what that release wrote.
	var major, initial pgtype.Text
	if policy.Method == "postgres-logical" {
		if err := restoredDatabaseOpens(v.Spec, policy, restored); err != nil {
			return db.SpecificationRevision{}, err
		}
		if !mountsVolume(v.Spec, policy.ResourceID, source) {
			return db.SpecificationRevision{}, domain.Invalid("restore.compatibility")
		}
		major.String = domain.PostgresMajor(restored)
		major.Valid = major.String != ""
		// An agent in the owner model restores every backup into it, a legacy database's too, so
		// the cutover also moves that database onto it. The draft's database is a copy: v.Spec's
		// own shares nothing with another caller.
		for i := range v.Spec.Applications {
			if a := &v.Spec.Applications[i]; a.ID == policy.ResourceID && a.Postgres != nil {
				moved := *a.Postgres
				moved.Roles = roles
				a.Postgres = &moved
				initial.String, initial.Valid = deployment.Hash(a.Postgres), true
			}
		}
	}
	q := db.New(tx)
	// The lock a new draft's volumes are checked under comes before the target's row.
	if err := store.LockVolumeDecisions(ctx, q, ws, env); err != nil {
		return db.SpecificationRevision{}, err
	}
	recorded, err := q.ClaimRestoreTarget(ctx, db.ClaimRestoreTargetParams{WorkspaceID: ws, EnvironmentID: env, UnitID: policy.Plan.UnitID, ID: target, PostgresMajor: major, PostgresInitHash: initial})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SpecificationRevision{}, domain.Conflict
	}
	if err != nil {
		return db.SpecificationRevision{}, err
	}
	if recorded.PostgresMajor != major || recorded.PostgresInitHash != initial {
		return db.SpecificationRevision{}, domain.Invalid("restore.compatibility")
	}
	if v.Spec.Compose != nil {
		oldName := ""
		for _, volume := range v.Spec.Volumes {
			if volume.ID == source {
				oldName = volume.Name
			}
		}
		next := "restored-" + run.String()
		if oldName == "" || target != composeproject.ResourceID(v.Unit, "volume", next) {
			return db.SpecificationRevision{}, domain.Conflict
		}
		bundle, err := composeproject.RenameVolume(ctx, *v.Spec.Compose, domain.VariableValues(v.Spec.Variables, nil), oldName, next)
		if err != nil {
			return db.SpecificationRevision{}, domain.Invalid("compose.restoreVolume")
		}
		v.Spec.Compose = &bundle
	} else {
		found := false
		for i := range v.Spec.Volumes {
			if v.Spec.Volumes[i].ID == source {
				v.Spec.Volumes[i].ID = target
				found = true
			}
		}
		if !found {
			return db.SpecificationRevision{}, domain.Conflict
		}
		for i := range v.Spec.Applications {
			for j := range v.Spec.Applications[i].Mounts {
				if v.Spec.Applications[i].Mounts[j].VolumeID == source {
					v.Spec.Applications[i].Mounts[j].VolumeID = target
				}
			}
		}
	}
	raw, _ := stablejson.Marshal(v.Spec)
	parsed, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		return db.SpecificationRevision{}, err
	}
	if err = store.RefuseDeletedMounts(ctx, q, ws, env, parsed); err != nil {
		return db.SpecificationRevision{}, err
	}
	h := sha256.Sum256(canonical)
	out, err := store.InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: uuid.New(), AuthorID: p.Account.ID, Specification: canonical, ContentHash: h[:]})
	if err != nil {
		return out, err
	}
	if boundary != nil {
		configuration, _ := stablejson.Marshal(boundary)
		if err = q.InsertBackupCutover(ctx, db.InsertBackupCutoverParams{WorkspaceID: ws, EnvironmentID: env, RevisionID: out.ID, PolicyID: policy.ID, SourcePlanID: boundary.SourcePlanID, Configuration: configuration}); err != nil {
			return out, err
		}
	}
	_, err = q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: ws, ID: env, DraftRevisionID: v.Revision, DraftRevisionID_2: out.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, domain.Conflict
	}
	return out, err
}
