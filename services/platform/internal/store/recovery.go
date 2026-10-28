package store

import (
	"context"
	"math"

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
	var ws uuid.UUID
	if e = tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", id).Scan(&ws); e != nil {
		return Normalize(e)
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
		return e
	}
	if e = LockServerDNS(ctx, tx, id); e != nil {
		return e
	}
	var prior int64
	if e = tx.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1 FOR UPDATE", id).Scan(&prior); e != nil {
		return e
	}
	if prior == math.MaxInt64 || next <= prior {
		return domain.Invalid("controllerGeneration")
	}
	var coordinated bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_reservations WHERE server_id=$1)`, id).Scan(&coordinated); e != nil {
		return e
	}
	if coordinated {
		return domain.Invalid("recovery.crossHostCheckpointRequired")
	}
	_, e = tx.Exec(ctx, `UPDATE servers SET controller_generation=$2,awaiting_transfer=true,credential_hash=NULL,previous_hash=NULL,previous_until=NULL,enrollment_hash=NULL,enrollment_expires=NULL,poll_id=NULL,poll_until=NULL,rotation_requested=false WHERE id=$1`, id, next)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE execution_operations SET state='cancelled',cancel_requested=true,note='Fenced after management recovery; inspect the host before resuming.' WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE git_sources SET configuration=configuration || '{"autoDeploy":false}'::jsonb WHERE workspace_id=$1`, ws); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE build_targets SET configuration=configuration || '{"autoBuild":false,"autoDeploy":false}'::jsonb WHERE workspace_id=$1`, ws); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE preview_policies SET enabled=false WHERE workspace_id=$1 AND server_id=$2`, ws, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE preview_requests SET state=CASE WHEN state='deleted' THEN state ELSE 'blocked' END,resume_after_cancel=false,cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL,note='Management recovery requires renewed preview approval.' WHERE workspace_id=$1 AND policy_id IN (SELECT id FROM preview_policies WHERE server_id=$2)`, ws, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE workspace_id=$1`, ws); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE pipeline_auto_deployments SET state='blocked',note='Management recovery requires renewed approval.' WHERE workspace_id=$1`, ws); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE server_domains SET approved_controller=NULL,state='review_required',note='Management recovery requires renewed approval.' WHERE server_id=$1", id); e != nil {
		return e
	}
	// River payloads are ephemeral delivery records; domain history stays intact.
	_, e = tx.Exec(ctx, `DELETE FROM river_job WHERE (kind='platform_dns_v1' AND args->>'server'=($1::uuid)::text) OR (kind='platform_execution_v1' AND args->>'id' IN (SELECT id::text FROM execution_operations WHERE server_id=$1)) OR (kind='platform_git_refresh_v1' AND args->>'workspace'=$2) OR (kind='platform_automatic_deployment_v1' AND args->>'ID' IN (SELECT id::text FROM pipeline_auto_deployments WHERE workspace_id=$3))`, id, ws.String(), ws)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
