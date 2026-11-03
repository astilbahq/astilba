package store

import (
	"context"
	"math"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// PrepareRecovery is an offline operator command after restoring an installation.
// It invalidates controller authority before the restored HTTP/worker process is
// started; host workloads and local schedule state are not contacted.
func (s *Store) PrepareRecovery(ctx context.Context, id uuid.UUID, next int64) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = RequireOffline(ctx, tx); e != nil {
		return e
	}
	q := db.New(tx)
	ws, e := q.RecoveryServerWorkspace(ctx, id)
	if e != nil {
		return Normalize(e)
	}
	if _, e = q.LockWorkspaceByID(ctx, ws); e != nil {
		return e
	}
	if e = LockServerDNS(ctx, tx, id); e != nil {
		return e
	}
	prior, e := q.LockServerControllerGeneration(ctx, db.LockServerControllerGenerationParams{WorkspaceID: ws, ServerID: id})
	if e != nil {
		return e
	}
	if prior == math.MaxInt64 || next <= prior {
		return domain.Invalid("controllerGeneration")
	}
	coordinated, e := q.ServerHasReservation(ctx, db.ServerHasReservationParams{WorkspaceID: ws, ServerID: id})
	if e != nil {
		return e
	}
	if coordinated {
		return domain.Invalid("recovery.crossHostCheckpointRequired")
	}
	if e = q.FenceServerController(ctx, db.FenceServerControllerParams{ControllerGeneration: next, WorkspaceID: ws, ServerID: id}); e != nil {
		return e
	}
	if e = q.CancelServerOperationsForRecovery(ctx, db.CancelServerOperationsForRecoveryParams{WorkspaceID: ws, ServerID: id}); e != nil {
		return e
	}
	if e = q.DisableWorkspaceGitAutoDeploy(ctx, ws); e != nil {
		return e
	}
	if e = q.DisableWorkspaceBuildAutomation(ctx, ws); e != nil {
		return e
	}
	if e = q.DisableServerPreviewPolicies(ctx, db.DisableServerPreviewPoliciesParams{WorkspaceID: ws, ServerID: id}); e != nil {
		return e
	}
	if e = q.BlockServerPreviewRequests(ctx, db.BlockServerPreviewRequestsParams{WorkspaceID: ws, ServerID: id}); e != nil {
		return e
	}
	if e = q.RevokeWorkspaceAutomationCredentials(ctx, ws); e != nil {
		return e
	}
	if e = q.BlockWorkspaceAutoDeployments(ctx, db.BlockWorkspaceAutoDeploymentsParams{Note: "Management recovery requires renewed approval.", WorkspaceID: ws}); e != nil {
		return e
	}
	if e = q.ResetServerDomainApproval(ctx, db.ResetServerDomainApprovalParams{WorkspaceID: ws, ServerID: id}); e != nil {
		return e
	}
	// River payloads are ephemeral delivery records; domain history stays intact.
	// inline-sql: river_job is owned by River.
	_, e = tx.Exec(ctx, `DELETE FROM river_job WHERE (kind='platform_dns_v1' AND args->>'server'=($1::uuid)::text) OR (kind='platform_execution_v1' AND args->>'id' IN (SELECT id::text FROM execution_operations WHERE workspace_id=$3 AND server_id=$1)) OR (kind='platform_git_refresh_v1' AND args->>'workspace'=$2) OR (kind='platform_automatic_deployment_v1' AND args->>'ID' IN (SELECT id::text FROM pipeline_auto_deployments WHERE workspace_id=$3))`, id, ws.String(), ws)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
