package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recoveryGate admits only the metadata-only open phase after every recovered
// participant has proved its staged machine, inventory and controller identity.
// The caller commits/snoozes while blocked, then resumes its saved normal phase.
func (s *Service) recoveryGate(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, root coordinationCommand) (bool, error) {
	var opened bool
	err := tx.QueryRow(ctx, `SELECT opened FROM cross_host_recoveries WHERE workspace_id=$1 AND parent_id=$2 FOR UPDATE`, ws, parent.Id).Scan(&opened)
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
	var proven int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM cross_host_recovery_servers r JOIN servers s ON s.workspace_id=r.workspace_id AND s.id=r.server_id WHERE r.workspace_id=$1 AND r.parent_id=$2 AND r.verified AND NOT s.awaiting_transfer AND NOT s.revoked AND s.machine_id=r.machine_id AND s.controller_generation=r.controller_generation`, ws, parent.Id).Scan(&proven); err != nil {
		return false, err
	}
	if proven != len(units) {
		_, err = tx.Exec(ctx, `UPDATE execution_operations SET state='needs_attention',note='Recovered execution remains held until every participant proves its exact machine, inventory and controller authority.' WHERE id=$1`, parent.Id)
		return true, err
	}
	if parent.RecoveryCheckpoint == nil {
		return true, domain.Invalid("recovery.checkpoint")
	}
	all := true
	for _, unit := range units {
		var controller int64
		if err = tx.QueryRow(ctx, `SELECT controller_generation FROM cross_host_recovery_servers WHERE workspace_id=$1 AND parent_id=$2 AND server_id=$3 AND verified`, ws, parent.Id, unit.Plan.ServerID).Scan(&controller); err != nil {
			return true, err
		}
		key := deployment.RecoveryOpenID(parent.Id, unit.Plan.UnitID, controller)
		var state api.ExecutionState
		err = tx.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1 AND parent_id=$2 AND server_id=$3`, key, parent.Id, unit.Plan.ServerID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			body, err := recoveryOpenBody(parent, root, unit)
			if err != nil {
				return true, err
			}
			raw, _ := json.Marshal(body)
			var generation int64
			if err = tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, parent.EnvironmentId).Scan(&generation); err != nil {
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
		_, err = tx.Exec(ctx, `UPDATE execution_operations SET state='needs_attention',note='Participant identities are verified; waiting for every exact-root replay-hold acknowledgement.' WHERE id=$1`, parent.Id)
		return true, err
	}
	_, err = tx.Exec(ctx, `UPDATE cross_host_recoveries SET opened=true WHERE workspace_id=$1 AND parent_id=$2`, ws, parent.Id)
	return false, err
}

func recoveryGateDelivery(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand) (bool, error) {
	if c.ParentId == nil {
		return true, nil
	}
	if c.Kind == "crosshost" && c.Crosshost != nil {
		var body deployment.CrossHostCommand
		if json.Unmarshal(*c.Crosshost, &body) == nil && body.Phase == "recovery-open" {
			return true, nil
		}
	}
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cross_host_recoveries WHERE parent_id=$1 AND NOT opened)`, *c.ParentId).Scan(&held)
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
