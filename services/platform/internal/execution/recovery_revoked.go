package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ignoreRevokedExports keeps a revoked host out of offline recovery's decisions. Such a host may be
// gone or hostile, so recovery needs nothing from it, and nothing it wrote counts: not its proofs,
// inventory, roots, private links or controller claim, any of which it could make up. Only its
// machine identity stays, so an export still can't stand in for another machine. loadRecoverySource
// already read only that, and its sealed capsules, from a server revoked in the restored
// management; this covers one only a newer capsule shows revoked.
func ignoreRevokedExports(ctx context.Context, tx pgx.Tx, before portability.Archive, source *recoverySource) error {
	revoked, err := recoveryRevokedServers(before, source.Archive)
	if err != nil {
		return err
	}
	for server, host := range source.Hosts {
		if !revoked[server] {
			continue
		}
		source.Hosts[server] = revokedEvidence(host)
		// inline-sql: recovery session temporary table
		if _, err = tx.Exec(ctx, `DELETE FROM recovery_input_proofs WHERE server_id=$1`, server); err != nil {
			return err
		}
	}
	return nil
}

// revokedEvidence is all of a revoked host's export that recovery keeps: which machine it is.
func revokedEvidence(host deployment.RecoveryEvidence) deployment.RecoveryEvidence {
	return deployment.RecoveryEvidence{Version: 1, ServerID: host.ServerID, MachineID: host.MachineID, Units: map[string]string{}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{}, Networks: []hostnetwork.Manifest{}}
}

// settleRevokedParticipants ends a recovered root's work on its revoked participants as revoking
// them does, once their frames are restored, so the coordinator carries on with the remaining hosts
// under the same rules as after any revocation.
func (s *Service) settleRevokedParticipants(ctx context.Context, tx pgx.Tx, ws uuid.UUID, roots map[uuid.UUID]recoveringRoot) error {
	settled := map[uuid.UUID]bool{}
	for _, root := range roots {
		revoked, err := revokedParticipants(ctx, tx, ws, recoveredUnits(root))
		if err != nil {
			return err
		}
		for server := range revoked {
			if settled[server] {
				continue
			}
			settled[server] = true
			if err = s.settleRevokedWork(ctx, tx, ws, server); err != nil {
				return err
			}
		}
	}
	return nil
}

// recoveredUnits are the units a recovered root still coordinates.
func recoveredUnits(root recoveringRoot) []deployment.DistributedUnit {
	if root.Body.Maintenance != nil {
		return root.Body.Plan.ActiveUnits()
	}
	return root.Body.Plan.Distributed.Units
}

// loadRevokedPrimaryRoots recovers an interrupted root whose primary is revoked from the checkpoints
// management sealed for it. Only the primary exports a root's chain, and a revoked host can't be
// asked for anything, but management kept every checkpoint it sealed up to its backup. What the
// primary sealed copies of beyond that extend the chain (extendRevokedChain). The root then resumes,
// and the remaining hosts end it as they do whenever a primary is revoked.
func loadRevokedPrimaryRoots(ctx context.Context, tx pgx.Tx, keys *secretbroker.Session, ws uuid.UUID, before portability.Archive, revoked map[uuid.UUID]bool, held map[uuid.UUID][]heldCapsule, out *recoverySource, bases *portability.RecoveryBases) error {
	supplied := map[uuid.UUID]bool{}
	// inline-sql: recovery session temporary table
	rows, err := tx.Query(ctx, `SELECT DISTINCT parent_id FROM recovery_input_checkpoints`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var parent uuid.UUID
		if err = rows.Scan(&parent); err != nil {
			rows.Close()
			return err
		}
		supplied[parent] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range before.Tables["execution_operations"] {
		command, err := recoveryValue[api.ExecutionCommand](row, "command")
		if err != nil {
			return err
		}
		state, err := recoveryValue[api.ExecutionState](row, "state")
		if err != nil {
			return err
		}
		if command.Kind != "crosshost" || command.ParentId != nil || Terminal(state) || !revoked[command.ServerId] || command.RecoveryCheckpoint == nil || supplied[command.Id] {
			continue
		}
		var known deployment.RecoveryDescriptor
		if recoveryDecode(*command.RecoveryCheckpoint, &known) != nil || known.Validate() != nil {
			return portability.ErrArchive
		}
		var last deployment.RecoveryDescriptor
		for sequence := int64(0); sequence <= known.Sequence; sequence++ {
			saved, err := db.New(tx).RecoveryCheckpointAt(ctx, db.RecoveryCheckpointAtParams{WorkspaceID: ws, ParentID: command.Id, Sequence: sequence})
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("an interrupted root on a revoked primary lacks a checkpoint management sealed for it")
			}
			if err != nil {
				return err
			}
			var d deployment.RecoveryDescriptor
			if recoveryDecode(saved.Descriptor, &d) != nil || d.Validate() != nil || d.ParentID != command.Id || d.Sequence != sequence || len(saved.Envelope) != d.Bytes || deployment.RecoveryDigest(saved.Envelope) != d.Digest {
				return errors.New("a checkpoint management sealed for a revoked primary's root is incomplete or divergent")
			}
			if err = addRecoveryCheckpoint(ctx, tx, keys, ws, out, bases, d, saved.Descriptor, saved.Envelope, command.ServerId); err != nil {
				return err
			}
			last = d
		}
		if err = extendRevokedChain(ctx, tx, keys, ws, out, bases, last, command.ServerId, held[command.Id]); err != nil {
			return err
		}
	}
	return nil
}

// heldCapsule is a revoked host's copy of a checkpoint of a root management kept a chain for.
type heldCapsule struct {
	descriptor    deployment.RecoveryDescriptor
	raw, envelope []byte
	host          uuid.UUID
}

// extendRevokedChain adds what a revoked primary sealed copies of beyond management's last
// checkpoint for a root, one sequence at a time while a copy is the root primary's, follows the last
// by digest, and opens under management's key. Such a copy is progress management itself recorded,
// so the host can replay it but not invent it, and a copy that doesn't qualify, from any host,
// counts for nothing. The first sequence with no qualifying copy, or with two different ones, ends
// the chain: withholding leaves the root where management's chain puts it, and two continuations
// both sealed for one step are taken as neither.
func extendRevokedChain(ctx context.Context, tx pgx.Tx, keys *secretbroker.Session, ws uuid.UUID, out *recoverySource, bases *portability.RecoveryBases, last deployment.RecoveryDescriptor, primary uuid.UUID, held []heldCapsule) error {
	for {
		next := map[uuid.UUID]heldCapsule{}
		for _, c := range held {
			if c.descriptor.Sequence != last.Sequence+1 || c.host != primary || c.descriptor.PreviousHash != last.Digest || !deployment.SameRecoveryRoot(last, c.descriptor) {
				continue
			}
			opened, err := keys.Open(secretbroker.Checkpoint(c.descriptor), c.envelope)
			clear(opened)
			// A capsule this keyring cannot open does not qualify; a session that ended is no
			// verdict on the capsule, and must not shorten the chain.
			if secretbroker.SessionFailure(err) {
				return err
			}
			if err == nil {
				next[c.descriptor.CheckpointID] = c
			}
		}
		if len(next) != 1 {
			return nil
		}
		for _, c := range next {
			if err := addRecoveryCheckpoint(ctx, tx, keys, ws, out, bases, c.descriptor, c.raw, c.envelope, primary); err != nil {
				return err
			}
			last = c.descriptor
		}
	}
}

// requireRecoverableRoots explains the one interrupted root recovery can't bring back: one a
// remaining host is still reserved for, whose primary is revoked, newer than anything management
// kept, and not in any supplied export. Nothing holds its frozen plan, so the remaining hosts can't
// be told how to end it.
func requireRecoverableRoots(source recoverySource, revoked map[uuid.UUID]bool) error {
	claimed := map[uuid.UUID]bool{}
	for _, host := range source.Hosts {
		for _, root := range host.Roots {
			if root.Primary && !revoked[host.ServerID] {
				claimed[root.Descriptor.ParentID] = true
			}
		}
	}
	for _, host := range source.Hosts {
		if revoked[host.ServerID] {
			continue
		}
		for _, root := range host.Roots {
			parent := root.Descriptor.ParentID
			if _, recovered := source.Roots[parent]; recovered || claimed[parent] || parent != host.ActiveParent {
				continue
			}
			if slices.ContainsFunc(root.Descriptor.Participants, func(server uuid.UUID) bool { return revoked[server] }) {
				return fmt.Errorf("interrupted deployment %s ran on a revoked server, and nothing in this backup or these exports holds its frozen plan, so the other servers it ran on can't be told how to end it. Revoke those servers too and leave out their exports, then deploy the environment to healthy servers", parent)
			}
		}
	}
	return nil
}
