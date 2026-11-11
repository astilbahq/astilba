package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// unpreparedNetworkNote explains the sweep's 'unprepared' hold, which lasts until the host reports,
// in its heartbeat, that it is prepared for managed networking, or the network changes.
const unpreparedNetworkNote = "This server reports that it is not prepared for managed networking or lacks the tools it needs, so it could not run a private network command. Nothing more is sent to it until it reports that it is ready or the network changes."

// duplicateNetworkKeyNote is also written by migration 00027, whose members identify again because
// it set no 'duplicate-key' hold. Agents generate a key per membership, so a repeated key means a
// modified agent, and peers could not tell its holders apart. The key stays with the member that
// already holds it.
const duplicateNetworkKeyNote = "This server reported the same private network key as another member. It must report its own key before private links can finish."

// networkReport applies a network command's outcome. live is false when recovery replays a host's
// proofs, whose claims checkRecoveryGenerationClaims has already held to its fixed limit.
func (s *Service) networkReport(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, r *api.AgentReport, live bool) error {
	if c.Kind != "network" {
		if r.Network != nil {
			return domain.Invalid("network")
		}
		return nil
	}
	if c.Network == nil {
		return domain.Invalid("network")
	}
	var cmd hostnetwork.Command
	if hostnetwork.Decode(*c.Network, &cmd) != nil || cmd.Validate() != nil || hostnetwork.Hash(cmd) != c.PlanHash {
		return domain.Invalid("network")
	}
	if r.State == "failed" && r.Network != nil && live {
		return networkRefusal(ctx, tx, ws, c, cmd, *r.Network)
	}
	// A failure with no receipt changed nothing on the host. Agents end a command this way when
	// the host is not prepared for managed networking or lacks what it needs. While its heartbeat
	// says so, the same command would fail the same way, so the sweep holds the member until the
	// host reports it is ready.
	if r.State == "failed" && live {
		return db.New(tx).HoldUnpreparedNetworkMember(ctx, db.HoldUnpreparedNetworkMemberParams{Note: unpreparedNetworkNote, RenewedNote: renewedNetworkApprovalNote, WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID})
	}
	if r.State != "succeeded" {
		return nil
	}
	if r.Network == nil {
		return domain.Invalid("network")
	}
	var receipt hostnetwork.Receipt
	if hostnetwork.Decode(*r.Network, &receipt) != nil || receipt.Refused || receipt.NetworkID != cmd.NetworkID || receipt.Identity.MembershipID != cmd.MembershipID || !hostnetwork.PublicKey(receipt.Identity.PublicKey) {
		return domain.Invalid("network")
	}
	if cmd.Action == "identify" {
		q := db.New(tx)
		taken, err := q.NetworkKeyHeldByAnotherMember(ctx, db.NetworkKeyHeldByAnotherMemberParams{WorkspaceID: ws, NetworkID: cmd.NetworkID, PublicKey: receipt.Identity.PublicKey, MembershipID: cmd.MembershipID})
		if err != nil {
			return err
		}
		if taken {
			r.State, r.Note = "failed", duplicateNetworkKeyNote
			return q.RefuseNetworkMemberKey(ctx, db.RefuseNetworkMemberKeyParams{Note: duplicateNetworkKeyNote, WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID})
		}
		return q.RecordNetworkMemberKey(ctx, db.RecordNetworkMemberKeyParams{PublicKey: receipt.Identity.PublicKey, WorkspaceID: ws, ServerID: c.ServerId, MembershipID: cmd.MembershipID})
	}
	if cmd.Manifest.ServerID != c.ServerId || receipt.Identity.PublicKey != cmd.Manifest.PublicKey {
		return domain.Invalid("network.receipt")
	}
	observed, err := reconciledNetworkReceipt(ctx, tx, c, cmd, receipt)
	if err != nil {
		return err
	}
	var installed []byte
	if err = tx.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE workspace_id=$1 AND server_id=$2 AND membership_id=$3 AND public_key=$4 FOR UPDATE`, ws, c.ServerId, cmd.MembershipID, receipt.Identity.PublicKey).Scan(&installed); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var known hostnetwork.Manifest
	if json.Unmarshal(installed, &known) == nil && known.Validate() == nil {
		if !hostnetwork.Later(known, observed) && hostnetwork.Hash(observed) != hostnetwork.Hash(known) {
			if hostnetwork.Later(observed, known) {
				return nil
			}
			return domain.Invalid("network.generation")
		}
	}
	n, err := readNetwork(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, member := range n.Members {
		if member.ServerID != c.ServerId {
			continue
		}
		if live {
			// The report stands: refusing it would stop the host taking commands, and any other
			// outcome would contradict the host's journal when recovery reads its export. Only
			// the counter and the record of what the host holds ignore the claim. The member
			// waits only if this command was its latest; otherwise the next sweep sends newer.
			if observed.Generation > generationClaimLimit(n.Generation, networkReceiptMargin) {
				r.Note = unaccountedNetworkGenerationNote
				if cmd.Manifest.Generation < member.Generation {
					return nil
				}
				return db.New(tx).SetNetworkMemberNote(ctx, db.SetNetworkMemberNoteParams{Note: unaccountedNetworkGenerationNote, WorkspaceID: ws, ServerID: c.ServerId})
			}
		}
		desired, ready, err := networkManifest(ctx, tx, ws, n, member)
		if err != nil {
			return err
		}
		converged := ready && sameNetworkConfiguration(desired, observed) && member.Controller == observed.ControllerGeneration && member.Controller == generation(c.ControllerGeneration)
		ack := int64(0)
		note := ""
		if converged {
			ack = member.Generation
		} else if member.Controller != observed.ControllerGeneration || member.Controller != generation(c.ControllerGeneration) {
			note = renewedNetworkApprovalNote
		}
		if _, err = tx.Exec(ctx, `UPDATE network_members SET acknowledged_generation=$3,manifest_hash=$4,note=$5,installed_manifest=$6,sweep_hold=NULL WHERE server_id=$1 AND membership_id=$2`, c.ServerId, cmd.MembershipID, ack, receipt.Hash, note, mustJSON(observed)); err != nil {
			return err
		}
		if converged || observed.Generation < member.Generation {
			return nil
		}
		// Partial removals can preserve additions whose configure receipt was
		// lost. The next full intent must not collide with observed authority.
		return advanceNetworkGeneration(ctx, tx, ws, observed.Generation)
	}
	return nil
}
