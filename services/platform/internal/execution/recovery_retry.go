package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) repeatedRecovery(ctx context.Context, tx pgx.Tx, in RecoveryImport, inputHash string, roots map[uuid.UUID]recoveringRoot, out *RecoveryPreview) (bool, error) {
	q := db.New(tx)
	handovers := map[uuid.UUID]deployment.RecoveryHandover{}
	parents := []uuid.UUID{}
	for parent, root := range roots {
		matches, err := q.CrossHostRecoveryApplied(ctx, db.CrossHostRecoveryAppliedParams{WorkspaceID: in.WorkspaceID, ParentID: parent, SourceHash: in.ExpectedState, InputHash: inputHash, OwnerID: in.OwnerID})
		if err != nil {
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
			stored, err := q.RecoveryServerHandover(ctx, db.RecoveryServerHandoverParams{WorkspaceID: in.WorkspaceID, ParentID: parent, ServerID: unit.Plan.ServerID})
			if err != nil {
				return false, err
			}
			var h deployment.RecoveryHandover
			if json.Unmarshal(stored.Handover, &h) != nil || h.Authority.Controller != stored.ControllerGeneration {
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
