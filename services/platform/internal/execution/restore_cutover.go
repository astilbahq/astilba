package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func readRestoreCutover(ctx context.Context, tx pgx.Tx, ws, env, revision uuid.UUID) (deployment.RestoreCutover, error) {
	var r deployment.RestoreCutover
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT configuration FROM backup_cutovers WHERE workspace_id=$1 AND environment_id=$2 AND revision_id=$3`, ws, env, revision).Scan(&raw); err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || r.Validate() != nil {
		return r, domain.Conflict
	}
	return r, nil
}

func validateRestoreRun(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, source deployment.Plan, r deployment.RestoreCutover) error {
	if source.ID != r.SourcePlanID || source.Hash != r.SourceHash {
		return domain.Conflict
	}
	p, err := loadBackupPolicy(ctx, tx, ws, env, r.PolicyID)
	if err != nil {
		return err
	}
	if p.Plan.Hash != source.PrimaryPlan().Hash {
		return domain.Conflict
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT status FROM backup_policies WHERE workspace_id=$1 AND environment_id=$2 AND id=$3`, ws, env, r.PolicyID).Scan(&raw); err != nil {
		return err
	}
	var status backup.Status
	if json.Unmarshal(raw, &status) != nil {
		return domain.Conflict
	}
	for _, run := range status.Runs {
		if run.ID != r.RunID {
			continue
		}
		old, err := backup.CutoverVolume(p, run)
		if err != nil || old != r.OldVolumeID || run.TargetID != r.TargetVolumeID || run.ManifestHash != r.ManifestHash {
			return domain.Invalid("restore.compatibility")
		}
		return nil
	}
	return domain.Conflict
}

func (s *Service) createRestoreReview(ctx context.Context, tx pgx.Tx, ws uuid.UUID, in api.VisualPlanRequest, v visualEnvironment, source deployment.Plan, network managedNetwork, r deployment.RestoreCutover) (deployment.Status, error) {
	if in.ReleaseOptions != nil {
		return deployment.Status{}, domain.Invalid("restore.releaseOptions")
	}
	if err := validateRestoreRun(ctx, tx, ws, source.EnvironmentID, source, r); err != nil {
		return deployment.Status{}, err
	}
	target, err := deployment.RestoredGroup(source, r, uuid.New(), v.Revision, time.Now().UTC().Add(15*time.Minute))
	if err != nil || deployment.Hash(target.Specification) != deployment.Hash(v.Spec) {
		return deployment.Status{}, domain.Conflict
	}
	// Restores keep existing endpoint permissions. Freeze the already approved
	// current network context without advancing a generation or adding a grant.
	target.Distributed.NetworkGeneration = network.Generation
	for index := range target.Distributed.Units {
		u := &target.Distributed.Units[index]
		found := false
		for _, member := range network.Members {
			if member.ServerID != u.Plan.ServerID || !member.Enabled {
				continue
			}
			manifest, ready, err := networkManifest(ctx, tx, ws, network, member)
			if err != nil {
				return deployment.Status{}, err
			}
			if !ready {
				return deployment.Status{}, domain.Conflict
			}
			u.Plan.Local.Network = manifest
			u.Plan = u.Plan.WithHash()
			found = true
		}
		if !found {
			return deployment.Status{}, domain.Conflict
		}
	}
	target = target.WithHash()
	if err = target.Validate(true); err != nil {
		return deployment.Status{}, domain.Invalid("restore.plan")
	}
	out, err := s.insertCrossHostReview(ctx, tx, ws, target)
	if err != nil {
		return out, err
	}
	err = copyNetworkPlanAssociations(ctx, tx, ws, source.ID, target.ID)
	return out, err
}

func (s *Service) admitRestoreApply(ctx context.Context, tx pgx.Tx, ws uuid.UUID, target deployment.Plan) error {
	if target.Restore == nil || target.Validate(true) != nil {
		return domain.Invalid("restore.plan")
	}
	bound, err := readRestoreCutover(ctx, tx, ws, target.EnvironmentID, target.RevisionID)
	if err != nil {
		return err
	}
	if deployment.Hash(bound) != deployment.Hash(*target.Restore) {
		return domain.Conflict
	}
	source, err := appliedVisualPlan(ctx, tx, ws, target.EnvironmentID)
	if err != nil {
		return err
	}
	if err = validateRestoreRun(ctx, tx, ws, target.EnvironmentID, source, bound); err != nil {
		return err
	}
	network, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	if network.ID != target.Distributed.NetworkID || network.Generation != target.Distributed.NetworkGeneration || network.State != "ready" {
		return domain.Conflict
	}
	for _, u := range target.ActiveUnits() {
		if _, err = s.visualServer(ctx, tx, ws, u.Plan.ServerID); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, u.Plan.ServerID, u.Plan); err != nil {
			return err
		}
		var online bool
		var controller int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(last_seen>now()-interval '45 seconds',false),controller_generation FROM servers WHERE workspace_id=$1 AND id=$2`, ws, u.Plan.ServerID).Scan(&online, &controller); err != nil {
			return err
		}
		if !online || u.Plan.Local.Network.ControllerGeneration != controller {
			return domain.Conflict
		}
		found := false
		for _, member := range network.Members {
			if member.ServerID != u.Plan.ServerID {
				continue
			}
			manifest, ready, err := networkManifest(ctx, tx, ws, network, member)
			if err != nil {
				return err
			}
			if !ready || hostnetwork.Hash(manifest) != hostnetwork.Hash(u.Plan.Local.Network) {
				return domain.Conflict
			}
			found = true
		}
		if !found {
			return domain.Conflict
		}
	}
	return nil
}

func (s *Service) insertRestoreMaintenance(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, target deployment.Plan) (api.Operation, error) {
	source, err := appliedVisualPlan(ctx, tx, ws, target.EnvironmentID)
	if err != nil {
		return api.Operation{}, err
	}
	r := target.Restore
	return s.insertMaintenance(ctx, tx, ws, key, source, maintenanceEffect{}, deployment.Maintenance{Kind: "restore", ResourceID: r.PolicyID, RunID: r.RunID, EffectHash: deployment.Hash(*r), Restore: r}, &target)
}
