package execution

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Call only after validating the host effect, in the transaction publishing it. The first call
// records when the change was committed, which places it in recovery's applied-history window.
func recordRuntimeOutcome(ctx context.Context, tx pgx.Tx, ws, env, operation uuid.UUID, outcome portability.RuntimeOutcome) error {
	var prior *uuid.UUID
	var priorGeneration *int64
	if err := tx.QueryRow(ctx, `SELECT e.runtime_operation_id,o.generation FROM environments e LEFT JOIN execution_operations o ON o.workspace_id=e.workspace_id AND o.environment_id=e.id AND o.id=e.runtime_operation_id WHERE e.workspace_id=$1 AND e.id=$2 FOR UPDATE OF e`, ws, env).Scan(&prior, &priorGeneration); err != nil {
		return err
	}
	var generation int64
	var kind *string
	var plan *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT generation,runtime_outcome,runtime_plan_id FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND id=$3 FOR UPDATE`, ws, env, operation).Scan(&generation, &kind, &plan); err != nil {
		return err
	}
	if generation <= 0 || prior != nil && *prior != operation && (priorGeneration == nil || *priorGeneration >= generation) {
		return errors.New("runtime outcome would replace a newer committed event")
	}
	if kind != nil && (*kind != outcome.Kind || (plan == nil) != (outcome.Plan == uuid.Nil) || plan != nil && *plan != outcome.Plan) {
		return errors.New("operation already committed a different runtime outcome")
	}
	var target *uuid.UUID
	if outcome.Plan != uuid.Nil {
		target = &outcome.Plan
	}
	if _, err := tx.Exec(ctx, `UPDATE execution_operations SET runtime_outcome=$4,runtime_plan_id=$5,runtime_recorded_at=COALESCE(runtime_recorded_at,clock_timestamp()) WHERE workspace_id=$1 AND environment_id=$2 AND id=$3`, ws, env, operation, outcome.Kind, target); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE environments SET runtime_operation_id=$3 WHERE workspace_id=$1 AND id=$2`, ws, env, operation)
	return err
}
