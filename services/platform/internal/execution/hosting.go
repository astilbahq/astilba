package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func recoveryCommand(c api.ExecutionCommand) bool {
	if c.Kind == "crosshost" && c.Crosshost != nil && c.ParentId != nil {
		var command deployment.CrossHostCommand
		if json.Unmarshal(*c.Crosshost, &command) == nil {
			if (command.Phase == "checkpoint" || command.Phase == "recovery-open" || command.Phase == "checkpoint-closed") && c.RecoveryCheckpoint != nil {
				return true
			}
			if command.Phase == "restore" && command.Maintenance != nil && command.Maintenance.Kind == "restore" {
				return true
			}
			if command.Phase == "effect" && command.Maintenance != nil && command.Maintenance.Kind == "upgrade" && (command.EffectAction == "return" || command.EffectAction == "reconcile") {
				return true
			}
			return command.Phase == "abort" || command.Phase == "retire" || command.Phase == "complete" || command.Phase == "resume-source" || command.Phase == "resume-target"
		}
	}
	if c.Kind == "network" && c.Network != nil {
		var n hostnetwork.Command
		return json.Unmarshal(*c.Network, &n) == nil && n.Action == "restrict" && n.Validate() == nil
	}
	if c.Kind == "visual" && c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil && p.Action == "preview-cleanup" && p.Plan.Preview != nil {
			return true
		}
	}
	if (c.Kind == "backup" || c.Kind == "schedule") && c.Backup != nil {
		var b backup.Command
		return json.Unmarshal(*c.Backup, &b) == nil && (b.Action == "restore" || b.Action == "check" || b.Action == "snapshots")
	}
	if c.Kind == "upgrade" && c.Upgrade != nil {
		var u upgrade.Command
		return json.Unmarshal(*c.Upgrade, &u) == nil && (u.Action == "return" || u.Action == "reconcile")
	}
	return false
}
func (s *Service) commandAdmission(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	if c.Kind == "crosshost" && c.ParentId != nil && c.Crosshost != nil {
		var body deployment.CrossHostCommand
		if json.Unmarshal(*c.Crosshost, &body) == nil && body.Phase == "checkpoint-closed" && body.ParentID == *c.ParentId && body.Digest() == c.PlanHash {
			return nil
		}
	}
	if !s.Store.Hosted {
		return nil
	}
	e, err := store.Entitlement(ctx, tx, ws)
	if err != nil {
		return err
	}
	if e.Active(time.Now()) || recoveryCommand(c) && e.Recoverable(time.Now()) {
		return nil
	}
	return store.SubscriptionRequired
}
