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
	var ws uuid.UUID
	if e = tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", id).Scan(&ws); e != nil {
		return Normalize(e)
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
		return e
	}
	var prior int64
	if e = tx.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1 FOR UPDATE", id).Scan(&prior); e != nil {
		return e
	}
	if prior == math.MaxInt64 || next <= prior {
		return domain.Invalid("controllerGeneration")
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
	if _, e = tx.Exec(ctx, `UPDATE pipeline_auto_deployments SET state='blocked',note='Management recovery requires renewed approval.' WHERE workspace_id=$1`, ws); e != nil {
		return e
	}
	// River payloads are ephemeral delivery records; domain history stays intact.
	_, e = tx.Exec(ctx, `DELETE FROM river_job WHERE (kind='platform_execution_v1' AND args->>'id' IN (SELECT id::text FROM execution_operations WHERE server_id=$1)) OR (kind='platform_git_refresh_v1' AND args->>'workspace'=$2) OR (kind='platform_automatic_deployment_v1' AND args->>'ID' IN (SELECT id::text FROM pipeline_auto_deployments WHERE workspace_id=$3))`, id, ws.String(), ws)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
