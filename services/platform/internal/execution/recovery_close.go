package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) closeUnreservedCheckpoint(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, root coordinationCommand) error {
	if parent.RecoveryCheckpoint == nil {
		return nil
	}
	var d deployment.RecoveryDescriptor
	if json.Unmarshal(*parent.RecoveryCheckpoint, &d) != nil || d.Validate() != nil {
		return domain.Invalid("recovery.checkpoint")
	}
	for _, unit := range root.Plan.Distributed.Units {
		if !unit.Plan.Local.Primary {
			continue
		}
		id := deployment.RecoveryCloseID(parent.Id, unit.Plan.UnitID)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND id=$2)`, ws, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		body := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: d.RootHash, Phase: "checkpoint-closed", Unit: unit}
		raw, _ := json.Marshal(body)
		var generation int64
		if err := tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, parent.EnvironmentId).Scan(&generation); err != nil {
			return err
		}
		c := api.ExecutionCommand{Id: id, ParentId: &parent.Id, ServerId: unit.Plan.ServerID, EnvironmentId: parent.EnvironmentId, ResourceId: unit.Plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, PlanHash: body.Digest(), Deadline: time.Now().Add(30 * time.Minute), Crosshost: rawMessage(raw), RecoveryCheckpoint: parent.RecoveryCheckpoint}
		_, err := s.insert(ctx, tx, ws, uuid.New(), nil, c)
		return err
	}
	return domain.Invalid("recovery.primary")
}
