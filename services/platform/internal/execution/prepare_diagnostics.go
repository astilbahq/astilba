package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Temporary CI diagnostics stay scoped to the prepare request. Markers are
// constants; request values, database messages and execution payloads are omitted.
type upgradePrepareDiagnosticKey struct{}

func logUpgradePrepareRejection(ctx context.Context, marker string) {
	if enabled, _ := ctx.Value(upgradePrepareDiagnosticKey{}).(bool); enabled {
		fmt.Println("upgrade prepare rejection:", marker)
	}
}

func logUpgradePrepareDatabaseFailure(ctx context.Context, err error) {
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" {
		return
	}
	// Classify before the workspace transaction normalizes uniqueness errors.
	// Never print server-supplied names or PostgreSQL message/detail fields.
	switch pgerr.ConstraintName {
	case "execution_active_environment":
		logUpgradePrepareRejection(ctx, "unique_active_environment")
	case "execution_active_server":
		logUpgradePrepareRejection(ctx, "unique_active_server")
	case "execution_active_restriction":
		logUpgradePrepareRejection(ctx, "unique_active_restriction")
	case "execution_environment_generation":
		logUpgradePrepareRejection(ctx, "unique_environment_generation")
	case "execution_operations_workspace_id_request_key_key":
		logUpgradePrepareRejection(ctx, "unique_request_key")
	case "cross_host_reservations_pkey":
		logUpgradePrepareRejection(ctx, "unique_server_reservation")
	case "execution_operations_pkey", "execution_parent_owner", "cross_host_operations_pkey":
		logUpgradePrepareRejection(ctx, "unique_operation_identity")
	default:
		logUpgradePrepareRejection(ctx, "unique_other_constraint")
	}
}
