package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// previewCleanupAuthority checks the authority to send a preview's cleanup to host, the host it runs
// on. The policy's approval is bound to the policy's host, so it covers only a preview running
// there; elsewhere the cleanup waits for an owner to ask for it, which binds it to that host.
func (s *Service) previewCleanupAuthority(ctx context.Context, tx pgx.Tx, p previewPolicy, r previewRecord, host uuid.UUID) error {
	if !r.Cleanup {
		if host != p.Server {
			return domain.Forbidden
		}
		return s.previewOwnerAuthority(ctx, tx, p)
	}
	if r.CleanupActor == nil || r.CleanupAuth == nil || r.CleanupController == nil {
		return domain.Forbidden
	}
	valid, err := db.New(tx).PreviewApproverHolds(ctx, db.PreviewApproverHoldsParams{WorkspaceID: p.Workspace, ActorID: *r.CleanupActor, AuthVersion: *r.CleanupAuth, ServerID: host, ControllerGeneration: *r.CleanupController})
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
	// A deleted request has nothing left to remove. Blocking it for want of authority would give it
	// back its slot, which a request its revoked host released could then never lose.
	if r.View.State == "deleted" {
		return nil
	}
	host, err := db.New(tx).PreviewHost(ctx, db.PreviewHostParams{WorkspaceID: p.Workspace, EnvironmentID: r.View.EnvironmentId, PolicyHost: p.Server})
	if err != nil {
		return err
	}
	if err := s.previewCleanupAuthority(ctx, tx, p, r, host); err != nil {
		if !errors.Is(err, domain.Forbidden) {
			return err
		}
		return s.previewState(ctx, tx, id, "blocked", "Cleanup needs renewed owner approval or a verified agent transfer. An owner's approval ends when they leave, stop being an owner or change their password or two-factor settings, and pauses while the workspace requires two-factor authentication they haven't set up.", 5*time.Minute)
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
	plan := deployment.Plan{ID: uuid.New(), ServerID: host, EnvironmentID: *r.View.EnvironmentId, UnitID: v.Unit, RevisionID: v.Revision, Recipe: deployment.PreviewRecipe, Specification: v.Spec, Preview: &domain.PreviewIdentity{RequestID: id, Generation: r.View.Generation, Limits: r.Config.Limits}, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}.WithHash()
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
	// A revocation ended this cleanup and released its preview, so a receipt the revoked host
	// kept is answered without changing what the owner was told.
	p, settled, err := removalCommand(ctx, tx, ws, c, "preview-cleanup")
	if err != nil || settled {
		return err
	}
	var purpose string
	if err := tx.QueryRow(ctx, "SELECT purpose FROM environments WHERE workspace_id=$1 AND id=$2", ws, c.EnvironmentId).Scan(&purpose); err != nil {
		return err
	}
	if purpose != "preview" {
		return domain.Forbidden
	}
	var requestEnvironment *uuid.UUID
	var requestGeneration int64
	if err := tx.QueryRow(ctx, `SELECT environment_id,generation FROM preview_requests WHERE workspace_id=$1 AND id=$2`, ws, p.Plan.Preview.RequestID).Scan(&requestEnvironment, &requestGeneration); err != nil {
		return err
	}
	if requestEnvironment == nil || *requestEnvironment != c.EnvironmentId || requestGeneration != p.Plan.Preview.Generation {
		// A reopened request can await allocation or own a new environment.
		// Replaying its committed removal cleans only the old imported rows.
		var sameRemoval bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM environments e JOIN execution_operations o ON o.workspace_id=e.workspace_id AND o.environment_id=e.id AND o.id=e.runtime_operation_id WHERE e.workspace_id=$1 AND e.id=$2 AND o.id=$3 AND o.runtime_outcome='removed')`, ws, c.EnvironmentId, c.Id).Scan(&sameRemoval); err != nil {
			return err
		}
		detached := requestEnvironment == nil && requestGeneration >= p.Plan.Preview.Generation
		replacement := requestEnvironment != nil && *requestEnvironment != c.EnvironmentId && requestGeneration > p.Plan.Preview.Generation
		if !sameRemoval || (!detached && !replacement) {
			return domain.Conflict
		}
	}
	if err := recordRuntimeOutcome(ctx, tx, ws, c.EnvironmentId, c.Id, portability.RuntimeOutcome{Kind: "removed"}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3 AND unit_id=$4", ws, c.EnvironmentId, c.ServerId, c.ResourceId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE environments SET applied_revision_id=NULL WHERE workspace_id=$1 AND id=$2`, ws, c.EnvironmentId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM route_claims WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3", ws, c.EnvironmentId, c.ServerId); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE managed_volumes SET deleted=true WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3", ws, c.EnvironmentId, c.ResourceId); err != nil {
		return err
	}
	if err := db.New(tx).DeleteEnvironmentSecretVersions(ctx, db.DeleteEnvironmentSecretVersionsParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId}); err != nil {
		return err
	}
	if err := db.New(tx).DeleteEnvironmentSecrets(ctx, db.DeleteEnvironmentSecretsParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET state='deleted',note='Preview resources and owned data were removed.',released_by_revocation=false,cleanup_at=NULL,updated_at=now(),next_check_at=now()+interval '1 day' WHERE workspace_id=$1 AND id=$2 AND environment_id=$3 AND generation=$4", ws, p.Plan.Preview.RequestID, c.EnvironmentId, p.Plan.Preview.Generation); err != nil {
		return err
	}
	return s.Store.Broker.DestroyEnvironmentKeys(ctx, db.New(tx), ws, c.EnvironmentId, "preview removed")
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
			return store.IfMissing(err, domain.Forbidden)
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

// previewCommandAllowed also verifies the request with its provider when given git to ask.
func (s *Service) previewCommandAllowed(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws uuid.UUID, c api.ExecutionCommand) (bool, error) {
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
	if cleanup {
		host, err := db.New(tx).PreviewHost(ctx, db.PreviewHostParams{WorkspaceID: ws, EnvironmentID: r.View.EnvironmentId, PolicyHost: p.Server})
		if err != nil {
			return false, err
		}
		if c.ServerId != host || r.View.State != "deleting" || r.View.OperationId == nil || *r.View.OperationId != c.Id {
			return false, nil
		}
		err = s.previewCleanupAuthority(ctx, tx, p, r, host)
		if errors.Is(err, domain.Forbidden) {
			return false, nil
		}
		return err == nil, err
	}
	// Builds and deployments run only on the policy's host.
	if c.ServerId != p.Server {
		return false, nil
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
	if git != nil {
		answer, err := git.pullRequest(ctx, tx, p.Connection, r.View.Number, "")
		if err != nil {
			return false, err
		}
		// A provider that refuses the repository for good, or no longer has the request (HTTP
		// 404), never shows it open again, and failing instead would fail every poll by the
		// command's host until its deadline.
		if _, final := finalProviderRefusal(answer.Err); final {
			return false, nil
		}
		if answer.Err != nil {
			return false, answer.Err
		}
		pr := answer.Value
		if !pr.Open || pr.Head != r.View.Head || pr.HeadRepository != r.View.HeadRepository || pr.BaseBranch != p.Config.Build.Branch {
			return false, nil
		}
	}
	return true, nil
}
