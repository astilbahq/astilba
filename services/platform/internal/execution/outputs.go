package execution

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func requireServiceOutputs(ctx context.Context, q releaseQuery, ws, server uuid.UUID, spec domain.Specification) error {
	if !domain.UsesServiceOutputs(spec) {
		return nil
	}
	var supported bool
	if err := q.QueryRow(ctx, "SELECT COALESCE((capabilities->>'serviceOutputs')::boolean,false) FROM servers WHERE workspace_id=$1 AND id=$2", ws, server).Scan(&supported); err != nil {
		return err
	}
	if !supported {
		return domain.Invalid("server.capabilities.serviceOutputs")
	}
	return nil
}
