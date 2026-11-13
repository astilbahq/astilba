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
		// The release that restored the target wrote its data, so the draft that will run on the
		// target, and the source the policy froze, must both open what that release wrote.
		var major pgtype.Text
		if policy.Method == "postgres-logical" {
			if err = restoredDatabaseOpens(v.Spec, policy, run); err != nil {
				return err
			}
			major.String = domain.PostgresMajor(run.RestoredImage())
			major.Valid = major.String != ""
		}
		source, err := backup.CutoverVolume(policy, run)
		if err != nil {
			return restoreIncompatible(err)
		}
		if policy.Method == "postgres-logical" && !mountsVolume(v.Spec, policy.ResourceID, source) {
			return domain.Invalid("restore.compatibility")
		}
		q := db.New(tx)
		recorded, err := q.ClaimRestoreTarget(ctx, db.ClaimRestoreTargetParams{WorkspaceID: ws, EnvironmentID: env, UnitID: policy.Plan.UnitID, ID: run.TargetID, PostgresMajor: major})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Conflict
		}
		if err != nil {
			return err
		}
		if recorded != major {
			return domain.Invalid("restore.compatibility")
		}
		var boundary *deployment.RestoreCutover
		if policy.Plan.Local != nil {
			boundary = &deployment.RestoreCutover{SourcePlanID: current.ID, SourceHash: current.Hash, PolicyID: policy.ID, RunID: run.ID, ManifestHash: run.ManifestHash, OldVolumeID: source, TargetVolumeID: run.TargetID}
		}
		if v.Spec.Compose != nil {
			oldName := ""
			for _, volume := range v.Spec.Volumes {
				if volume.ID == source {
					oldName = volume.Name
				}
			}
			next := "restored-" + run.ID.String()
			if oldName == "" || run.TargetID != composeproject.ResourceID(v.Unit, "volume", next) {
				return domain.Conflict
			}
			bundle, err := composeproject.RenameVolume(ctx, *v.Spec.Compose, domain.VariableValues(v.Spec.Variables, nil), oldName, next)
			if err != nil {
				return domain.Invalid("compose.restoreVolume")
			}
			v.Spec.Compose = &bundle
		} else {
			found := false
			for i := range v.Spec.Volumes {
				if v.Spec.Volumes[i].ID == source {
					v.Spec.Volumes[i].ID = run.TargetID
					found = true
				}
			}
			if !found {
				return domain.Conflict
			}
			for i := range v.Spec.Applications {
				for j := range v.Spec.Applications[i].Mounts {
					if v.Spec.Applications[i].Mounts[j].VolumeID == source {
						v.Spec.Applications[i].Mounts[j].VolumeID = run.TargetID
					}
				}
			}
		}

		raw, _ = stablejson.Marshal(v.Spec)
		_, canonical, err := domain.ParseSpec(raw)
		if err != nil {
			return err
		}
		h := sha256.Sum256(canonical)
		out, err = store.InsertRevision(ctx, q, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: uuid.New(), AuthorID: p.Account.ID, Specification: canonical, ContentHash: h[:]})
		if err != nil {
			return err
		}
		if boundary != nil {
			configuration, _ := stablejson.Marshal(boundary)
			if err = q.InsertBackupCutover(ctx, db.InsertBackupCutoverParams{WorkspaceID: ws, EnvironmentID: env, RevisionID: out.ID, PolicyID: id, SourcePlanID: boundary.SourcePlanID, Configuration: configuration}); err != nil {
				return err
			}
		}
		_, err = q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: ws, ID: env, DraftRevisionID: in.BaseRevisionId, DraftRevisionID_2: out.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Conflict
		}
		return err
	})
	return out, err
}
