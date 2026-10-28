package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) repeatedRecovery(ctx context.Context, tx pgx.Tx, in RecoveryImport, inputHash string, roots map[uuid.UUID]recoveringRoot, out *RecoveryPreview) (bool, error) {
	handovers := map[uuid.UUID]deployment.RecoveryHandover{}
	parents := []uuid.UUID{}
	for parent, root := range roots {
		var matches bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_recoveries WHERE workspace_id=$1 AND parent_id=$2 AND source_hash=$3 AND input_hash=$4 AND owner_id=$5)`, in.WorkspaceID, parent, in.ExpectedState, inputHash, in.OwnerID).Scan(&matches); err != nil {
			return false, err
		}
		if !matches {
			return false, nil
		}
		units := root.Body.Plan.Distributed.Units
		if root.Body.Maintenance != nil {
			units = root.Body.Plan.ActiveUnits()
		}
		for _, unit := range units {
			var raw []byte
			var current int64
			err := tx.QueryRow(ctx, `SELECT r.handover,s.controller_generation FROM cross_host_recovery_servers r JOIN servers s ON s.workspace_id=r.workspace_id AND s.id=r.server_id WHERE r.workspace_id=$1 AND r.parent_id=$2 AND r.server_id=$3`, in.WorkspaceID, parent, unit.Plan.ServerID).Scan(&raw, &current)
			if err != nil {
				return false, err
			}
			var h deployment.RecoveryHandover
			if json.Unmarshal(raw, &h) != nil || h.Authority.Controller != current {
				return false, errors.New("recovery was applied, but controller authority has since advanced; inspect current state")
			}
			handovers[unit.Plan.ServerID] = h
		}
		parents = append(parents, parent)
	}
	out.Handovers, out.Parents = handovers, parents
	out.Applied, out.AlreadyApplied = true, true
	return true, nil
}
