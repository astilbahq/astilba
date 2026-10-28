package execution

import (
	"context"
	"encoding/json"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func standaloneRestrictionFrame(c api.ExecutionCommand) bool {
	var command hostnetwork.Command
	return c.ParentId == nil && c.Kind == "network" && c.Action == "inspect" && c.EnvironmentId == uuid.Nil && c.Image == "" && c.Release == nil && c.RecoveryCheckpoint == nil && c.Visual == nil && c.Pipeline == nil && c.Crosshost == nil && c.Backup == nil && c.Upgrade == nil && c.Generation > 0 && !c.Deadline.IsZero() && c.Network != nil && hostnetwork.Decode(*c.Network, &command) == nil && command.Action == "restrict" && command.Baseline != nil && command.Validate() == nil && command.Manifest.ServerID == c.ServerId && command.NetworkID == c.ResourceId && hostnetwork.Hash(command) == c.PlanHash
}

func networkRestrictionScope(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, command hostnetwork.Command) (hostnetwork.Manifest, uuid.UUID, error) {
	if c.ParentId == nil {
		if command.Baseline == nil {
			return hostnetwork.Manifest{}, uuid.Nil, domain.Invalid("network.baseline")
		}
		return *command.Baseline, uuid.Nil, nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1 AND server_id IN (SELECT id FROM servers WHERE workspace_id=(SELECT workspace_id FROM servers WHERE id=$2))`, *c.ParentId, c.ServerId).Scan(&raw); err != nil {
		return hostnetwork.Manifest{}, uuid.Nil, err
	}
	var parent api.ExecutionCommand
	var root coordinationCommand
	if json.Unmarshal(raw, &parent) != nil || parent.Crosshost == nil || json.Unmarshal(*parent.Crosshost, &root) != nil || Hash(root) != parent.PlanHash || root.Plan.Distributed == nil {
		return hostnetwork.Manifest{}, uuid.Nil, domain.Invalid("network.parent")
	}
	for _, unit := range root.Plan.Distributed.Units {
		if unit.Plan.ServerID == c.ServerId && unit.Plan.Local != nil {
			return unit.Plan.Local.Network, unit.Plan.UnitID, nil
		}
	}
	return hostnetwork.Manifest{}, uuid.Nil, domain.Invalid("network.unit")
}

func reconciledNetworkReceipt(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, cmd hostnetwork.Command, receipt hostnetwork.Receipt) (hostnetwork.Manifest, error) {
	m := *cmd.Manifest
	if receipt.Before == nil && receipt.Effective == nil {
		if receipt.Generation != m.Generation || receipt.Hash != hostnetwork.Hash(m) || receipt.Disabled != m.Disabled {
			return m, domain.Invalid("network.receipt")
		}
		return m, nil
	}
	if cmd.Action != "restrict" || receipt.Before == nil || receipt.Effective == nil {
		return m, domain.Invalid("network.receipt")
	}
	baseline, unit, err := networkRestrictionScope(ctx, tx, c, cmd)
	if err != nil {
		return m, err
	}
	if receipt.Effective.ControllerGeneration > generation(c.ControllerGeneration) {
		return m, domain.Invalid("network.controller")
	}
	effective, err := hostnetwork.ReconcileRestriction(*receipt.Before, baseline, m, unit, receipt.Effective.ControllerGeneration)
	if err != nil || hostnetwork.Hash(effective) != hostnetwork.Hash(*receipt.Effective) || receipt.Generation != effective.Generation || receipt.Hash != hostnetwork.Hash(effective) || receipt.Disabled != effective.Disabled {
		return m, domain.Invalid("network.receipt")
	}
	return effective, nil
}
