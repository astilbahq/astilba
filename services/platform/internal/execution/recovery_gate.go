package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recoveryGate admits only the metadata-only open phase after every recovered
// participant has proved its staged machine, inventory and controller identity.
// The caller commits/snoozes while blocked, then resumes its saved normal phase.
func (s *Service) recoveryGate(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, root coordinationCommand) (bool, error) {
	q := db.New(tx)
	opened, err := q.LockCrossHostRecoveryOpened(ctx, db.LockCrossHostRecoveryOpenedParams{WorkspaceID: ws, ParentID: parent.Id})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && opened {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	units := root.Plan.Distributed.Units
	if root.Maintenance != nil {
		units = root.Plan.ActiveUnits()
	}
	proven, err := q.CountProvenRecoveryServers(ctx, db.CountProvenRecoveryServersParams{WorkspaceID: ws, ParentID: parent.Id})
	if err != nil {
		return false, err
	}
	if proven != int64(len(units)) {
		return true, q.HoldRecoveredOperation(ctx, db.HoldRecoveredOperationParams{Note: "Recovered execution remains held until every participant proves its exact machine, inventory and controller authority.", WorkspaceID: ws, ID: parent.Id})
	}
	if parent.RecoveryCheckpoint == nil {
		return true, domain.Invalid("recovery.checkpoint")
	}
	all := true
	for _, unit := range units {
		controller, err := q.VerifiedRecoveryController(ctx, db.VerifiedRecoveryControllerParams{WorkspaceID: ws, ParentID: parent.Id, ServerID: unit.Plan.ServerID})
		if err != nil {
			return true, err
		}
		key := deployment.RecoveryOpenID(parent.Id, unit.Plan.UnitID, controller)
		state, err := q.RecoveryOpenState(ctx, db.RecoveryOpenStateParams{WorkspaceID: ws, ID: key, ParentID: parent.Id, ServerID: unit.Plan.ServerID})
		if errors.Is(err, pgx.ErrNoRows) {
			body, err := recoveryOpenBody(parent, root, unit)
			if err != nil {
				return true, err
			}
			raw, _ := stablejson.Marshal(body)
			generation, err := q.AdvanceEnvironmentExecutionGeneration(ctx, db.AdvanceEnvironmentExecutionGenerationParams{WorkspaceID: ws, ID: parent.EnvironmentId})
			if err != nil {
				return true, err
			}
			c := api.ExecutionCommand{Id: key, ParentId: &parent.Id, ServerId: unit.Plan.ServerID, EnvironmentId: parent.EnvironmentId, ResourceId: unit.Plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, PlanHash: body.Digest(), Deadline: time.Now().Add(30 * time.Minute), Crosshost: rawMessage(raw), RecoveryCheckpoint: parent.RecoveryCheckpoint}
			if _, err = s.insert(ctx, tx, ws, uuid.New(), nil, c); err != nil {
				return true, err
			}
			all = false
		} else if err != nil {
			return true, err
		} else if state != "succeeded" {
			all = false
		}
	}
	if !all {
		return true, q.HoldRecoveredOperation(ctx, db.HoldRecoveredOperationParams{Note: "Participant identities are verified; waiting for every exact-root replay-hold acknowledgement.", WorkspaceID: ws, ID: parent.Id})
	}
	return false, q.OpenCrossHostRecovery(ctx, db.OpenCrossHostRecoveryParams{WorkspaceID: ws, ParentID: parent.Id})
}

func recoveryGateDelivery(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) (bool, error) {
	if c.ParentId == nil {
		return true, nil
	}
	if c.Kind == "crosshost" && c.Crosshost != nil {
		var body deployment.CrossHostCommand
		if json.Unmarshal(*c.Crosshost, &body) == nil && body.Phase == "recovery-open" {
			return true, nil
		}
	}
	held, err := db.New(tx).CrossHostRecoveryHeld(ctx, db.CrossHostRecoveryHeldParams{WorkspaceID: ws, ParentID: *c.ParentId})
	return !held, err
}

func recoveryOpenBody(parent api.ExecutionCommand, root coordinationCommand, unit deployment.DistributedUnit) (deployment.CrossHostCommand, error) {
	body := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: root.Plan.Hash, Phase: "recovery-open", Unit: unit}
	if root.Maintenance != nil {
		body.GroupHash = parent.PlanHash
		for _, candidate := range root.Maintenance.Units {
			if candidate.Unit.Plan.ServerID == unit.Plan.ServerID {
				intent := candidate.Intent
				body.Maintenance = &intent
			}
		}
		if body.Maintenance == nil {
			return body, domain.Invalid("recovery.participant")
		}
	}
	return body, nil
}
