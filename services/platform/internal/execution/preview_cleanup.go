package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) previewCleanupAuthority(ctx context.Context, tx pgx.Tx, p previewPolicy, r previewRecord) error {
	if !r.Cleanup {
		return s.previewOwnerAuthority(ctx, tx, p)
	}
	if r.CleanupActor == nil || r.CleanupAuth == nil || r.CleanupController == nil {
		return domain.Forbidden
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN accounts a ON a.id=m.account_id JOIN servers s ON s.workspace_id=m.workspace_id WHERE m.workspace_id=$1 AND m.account_id=$2 AND m.role='owner' AND a.auth_version=$3 AND s.id=$4 AND s.controller_generation=$5 AND s.preview_only AND s.role='combined' AND NOT s.revoked AND NOT s.awaiting_transfer AND COALESCE((s.capabilities->>'previews')::boolean,false))`, p.Workspace, *r.CleanupActor, *r.CleanupAuth, p.Server, *r.CleanupController).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Forbidden
	}
	return nil
}
func (s *Service) reconcilePreviewCleanup(ctx context.Context, tx pgx.Tx, p previewPolicy, r previewRecord) error {
	id := r.View.Id
	if err := s.previewCleanupAuthority(ctx, tx, p, r); err != nil {
		if !errors.Is(err, domain.Forbidden) {
			return err
		}
		return s.previewState(ctx, tx, id, "blocked", "Cleanup needs renewed owner approval or a verified agent transfer.", 5*time.Minute)
	}
	if r.View.State == "deleted" {
		return nil
	}
	if err := s.cancelPreviewEnvironment(ctx, tx, p.Workspace, r.View.EnvironmentId); err != nil {
		return err
	}
	busy, err := previewBusy(ctx, tx, p.Workspace, r.View.EnvironmentId)
	if err != nil {
		return err
	}
	if r.View.State == "deleting" && r.View.OperationId != nil {
		var state string
		if err = tx.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", *r.View.OperationId).Scan(&state); err != nil {
			return err
		}
		if state == "succeeded" {
			return nil
		}
		if state == "failed" || state == "cancelled" {
			return s.previewState(ctx, tx, id, "blocked", "Cleanup stopped. Review the operation before renewing cleanup approval.", 5*time.Minute)
		}
		return s.previewState(ctx, tx, id, "deleting", "Waiting for the agent to confirm removal; the resource slot remains reserved.", time.Minute)
	}
	if busy {
		return s.previewState(ctx, tx, id, "closing", "Waiting for cancellation acknowledgement before cleanup.", time.Minute)
	}
	if r.View.EnvironmentId == nil {
		return s.previewState(ctx, tx, id, "deleted", "No preview resources were allocated.", 24*time.Hour)
	}
	v, err := loadVisualEnvironment(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId)
	if err != nil {
		return err
	}
	if v.Purpose != "preview" {
		return domain.Forbidden
	}
	if r.Config == nil {
		return domain.Invalid("preview.configuration")
	}
	plan := deployment.Plan{ID: uuid.New(), ServerID: p.Server, EnvironmentID: *r.View.EnvironmentId, UnitID: v.Unit, RevisionID: v.Revision, Recipe: deployment.PreviewRecipe, Specification: v.Spec, Preview: &domain.PreviewIdentity{RequestID: id, Generation: r.View.Generation, Limits: r.Config.Limits}, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}.WithHash()
	op, err := s.insertVisual(ctx, tx, p.Workspace, uuid.New(), deployment.Payload{Action: "preview-cleanup", Plan: plan})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET operation_id=$2 WHERE id=$1", id, op.Id); err != nil {
		return err
	}
	return s.previewState(ctx, tx, id, "deleting", "Removing this preview's routes, services and owned data.", time.Minute)
}

// Used for normal reports and authenticated transfer receipts. Immutable command
// identity, rather than a caller-supplied resource list, determines the cleanup.
func (s *Service) finalizePreviewCleanup(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	var p deployment.Payload
	if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != "preview-cleanup" || p.Plan.Preview == nil || p.Digest() != c.PlanHash || p.Plan.ServerID != c.ServerId || p.Plan.EnvironmentID != c.EnvironmentId || p.Plan.UnitID != c.ResourceId || p.Plan.Validate(false) != nil {
		return domain.Forbidden
	}
	var purpose string
	if err := tx.QueryRow(ctx, "SELECT purpose FROM environments WHERE workspace_id=$1 AND id=$2", ws, c.EnvironmentId).Scan(&purpose); err != nil {
		return err
	}
	if purpose != "preview" {
		return domain.Forbidden
	}
	if _, err := tx.Exec(ctx, "DELETE FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3 AND unit_id=$4", ws, c.EnvironmentId, c.ServerId, c.ResourceId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM route_claims WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3", ws, c.EnvironmentId, c.ServerId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE managed_volumes SET deleted=true WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3", ws, c.EnvironmentId, c.ResourceId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM secret_versions v USING secrets s WHERE s.workspace_id=$1 AND s.environment_id=$2 AND v.workspace_id=s.workspace_id AND v.secret_id=s.id", ws, c.EnvironmentId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM secrets WHERE workspace_id=$1 AND environment_id=$2", ws, c.EnvironmentId); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE preview_requests SET state='deleted',note='Preview resources and owned data were removed.',cleanup_at=NULL,updated_at=now(),next_check_at=now()+interval '1 day' WHERE workspace_id=$1 AND id=$2 AND environment_id=$3 AND generation=$4", ws, p.Plan.Preview.RequestID, c.EnvironmentId, p.Plan.Preview.Generation)
	return err
}
func (s *Service) acceptPreviewCleanupProofs(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID, receipts *[]api.PreviewCleanupReceipt) error {
	if receipts == nil {
		return nil
	}
	if len(*receipts) > 100 {
		return domain.Invalid("preview.cleanupProofs")
	}
	seen := map[uuid.UUID]bool{}
	for _, proof := range *receipts {
		if seen[proof.OperationId] {
			return domain.Invalid("preview.cleanupProofs")
		}
		seen[proof.OperationId] = true
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT command FROM execution_operations WHERE workspace_id=$1 AND server_id=$2 AND id=$3", ws, server, proof.OperationId).Scan(&raw); err != nil {
			return domain.Forbidden
		}
		var c api.ExecutionCommand
		if json.Unmarshal(raw, &c) != nil || c.PlanHash != proof.Digest || c.Id != proof.OperationId || c.ServerId != server {
			return domain.Forbidden
		}
		if err := s.finalizePreviewCleanup(ctx, tx, ws, c); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) previewPlanIdentity(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID) (*domain.PreviewIdentity, error) {
	var id uuid.UUID
	var generation int64
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT id,generation,configuration FROM preview_requests WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&id, &generation, &raw)
	if err != nil {
		return nil, err
	}
	var cfg previewConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &domain.PreviewIdentity{RequestID: id, Generation: generation, Limits: cfg.Limits}, nil
}

func (s *Service) previewCommandAllowed(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, verify bool) (bool, error) {
	var id uuid.UUID
	var generation int64
	var head string
	var repo int64
	cleanup := false
	if c.Pipeline != nil {
		var p pipeline.Command
		if err := json.Unmarshal(*c.Pipeline, &p); err != nil {
			return false, err
		}
		if p.Preview == nil {
			return true, nil
		}
		id = p.Preview.RequestID
		generation = p.Preview.Generation
		head = p.Commit
		repo = p.Preview.HeadRepository
	}
	if c.Visual != nil {
		var p deployment.Payload
		if err := json.Unmarshal(*c.Visual, &p); err != nil {
			return false, err
		}
		if p.Plan.Preview == nil {
			return true, nil
		}
		id = p.Plan.Preview.RequestID
		generation = p.Plan.Preview.Generation
		cleanup = p.Action == "preview-cleanup"
	}
	if id == uuid.Nil {
		return true, nil
	}
	r, err := loadPreview(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.View.EnvironmentId == nil || *r.View.EnvironmentId != c.EnvironmentId || r.View.Generation != generation {
		return false, nil
	}
	p, err := scanPreviewPolicy(tx.QueryRow(ctx, "SELECT "+previewPolicyColumns+" FROM preview_policies WHERE workspace_id=$1 AND id=$2", ws, r.Policy))
	if err != nil {
		return false, err
	}
	if c.ServerId != p.Server {
		return false, nil
	}
	if cleanup {
		if r.View.State != "deleting" || r.View.OperationId == nil || *r.View.OperationId != c.Id {
			return false, nil
		}
		err = s.previewCleanupAuthority(ctx, tx, p, r)
		if errors.Is(err, domain.Forbidden) {
			return false, nil
		}
		return err == nil, err
	}
	if err = s.previewAuthority(ctx, tx, p); err != nil {
		if errors.Is(err, domain.Forbidden) || errors.Is(err, store.SubscriptionRequired) {
			return false, nil
		}
		return false, err
	}
	if r.Cleanup || r.Closed != nil || r.View.State == "deleted" || r.View.State == "closing" || r.View.State == "deleting" || r.PolicyGeneration != p.Generation {
		return false, nil
	}
	if head != "" && (head != r.View.Head || repo != r.View.HeadRepository) {
		return false, nil
	}
	if r.View.Fork && (r.ApprovedHead != r.View.Head || r.ApprovedRepo != r.View.HeadRepository || r.ApprovedGeneration != p.Generation) {
		return false, nil
	}
	if verify {
		provider, _, err := s.previewProvider(ctx, tx, ws, p.Connection)
		if err != nil {
			return false, err
		}
		pr, err := provider.PullRequest(ctx, r.View.Number)
		if err != nil {
			return false, err
		}
		if !pr.Open || pr.Head != r.View.Head || pr.HeadRepository != r.View.HeadRepository || pr.BaseBranch != p.Config.Build.Branch {
			return false, nil
		}
	}
	return true, nil
}
