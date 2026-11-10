package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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

// networkRefusal stops the sweep repeating a command a host refused without changing anything. A
// refusal can only hold back the member that sent it, so a compromised host gains nothing from one.
// Whatever the reason, the member is still sent removals. A host checks routes only for a
// configure, so a reason on a removal is refused. A route conflict comes with what the host holds,
// which scopes those removals when management has no record: without one a revoked peer would
// keep its access to this host for as long as the conflict stands. The claim is the host's
// about itself, which a modified agent could misstate anyway by ignoring a removal.
func networkRefusal(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, cmd hostnetwork.Command, raw jsontext.Value) error {
	var receipt hostnetwork.Receipt
	if hostnetwork.Decode(raw, &receipt) != nil || !receipt.Refused || c.ParentId != nil || cmd.Manifest == nil || receipt.NetworkID != cmd.NetworkID || receipt.Identity.MembershipID != cmd.MembershipID {
		return domain.Invalid("network.refusal")
	}
	q := db.New(tx)
	note := refusedNetworkConfigureNote
	switch {
	case receipt.Reason == "" && cmd.Action == "restrict":
		return q.ForgetInstalledNetwork(ctx, db.ForgetInstalledNetworkParams{WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID})
	case receipt.Reason == hostnetwork.RouteConflict && cmd.Action == "configure":
		note = routeConflictNetworkNote
		if receipt.Before != nil {
			held := *receipt.Before
			// A host describes only what it holds for this membership, which it admitted under
			// this or an earlier controller and generation, so anything else is not a refusal.
			if held.Validate() != nil || held.NetworkID != cmd.NetworkID || held.MembershipID != cmd.MembershipID || held.ServerID != c.ServerId || held.PublicKey != cmd.Manifest.PublicKey || hostnetwork.Later(*cmd.Manifest, held) {
				return domain.Invalid("network.refusal")
			}
			raw := mustJSON(held)
			issued, err := q.NetworkGrantsIssuedTo(ctx, db.NetworkGrantsIssuedToParams{Manifest: raw, WorkspaceID: ws, ServerID: c.ServerId})
			if err != nil {
				return err
			}
			// A peer or grant this management never made can't be one it sent. An honest host
			// can still hold one after management is restored from an older backup, so the
			// refusal stands and only the description is not kept. Nor is one older than the
			// record management has, which no host holds after confirming that record: replayed
			// on every refusal, it would have management send the same removal again, and the
			// generation advance that follows it, without end.
			stale := false
			if record, err := q.MemberInstalledManifest(ctx, db.MemberInstalledManifestParams{WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			} else {
				var known hostnetwork.Manifest
				stale = json.Unmarshal(record, &known) == nil && known.Validate() == nil && known.MembershipID == cmd.MembershipID && hostnetwork.Later(held, known)
			}
			if issued && !stale {
				if err := q.RecordRefusingNetworkMember(ctx, db.RecordRefusingNetworkMemberParams{InstalledManifest: raw, WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID, Generation: cmd.Manifest.Generation}); err != nil {
					return err
				}
			}
		}
	case receipt.Reason != "":
		return domain.Invalid("network.refusal")
	}
	return q.NoteRefusedNetworkConfigure(ctx, db.NoteRefusedNetworkConfigureParams{Note: note, WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID, Generation: cmd.Manifest.Generation})
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

func sameNetworkConfiguration(a, b hostnetwork.Manifest) bool {
	// Empty grant lists mean the same thing after either JSON null or [] decoding.
	// Normalize copies only; frozen command and receipt digests retain their encoding.
	for _, m := range []*hostnetwork.Manifest{&a, &b} {
		if len(m.Peers) == 0 {
			m.Peers = nil
		}
		if len(m.Exports) == 0 {
			m.Exports = nil
		}
		if len(m.Egress) == 0 {
			m.Egress = nil
		}
	}
	return hostnetwork.Hash(a) == hostnetwork.Hash(b)
}

func mustJSON(v any) []byte { b, _ := stablejson.Marshal(v); return b }
