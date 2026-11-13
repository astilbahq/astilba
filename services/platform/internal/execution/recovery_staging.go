package execution

import (
	"context"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

func (s *Service) finishRecoveryStaging(ctx context.Context, tx pgx.Tx, in RecoveryImport, source recoverySource, roots map[uuid.UUID]recoveringRoot) error {
	if s.Queue == nil {
		return errors.New("offline recovery requires the configured durable queue")
	}
	q := db.New(tx)
	// inline-sql: recovery session temporary table
	if _, err := tx.Exec(ctx, `DELETE FROM cross_host_reservations WHERE workspace_id=$1 AND parent_id NOT IN (SELECT id FROM recovery_selected_roots)`, in.WorkspaceID); err != nil {
		return err
	}
	for parent, root := range roots {
		units := recoveredUnits(root)
		revoked, err := revokedParticipants(ctx, tx, in.WorkspaceID, units)
		if err != nil {
			return err
		}
		for _, unit := range withoutRevoked(units, revoked) {
			reserved, err := q.ReserveRecoveredServer(ctx, db.ReserveRecoveredServerParams{ServerID: unit.Plan.ServerID, WorkspaceID: in.WorkspaceID, ParentID: parent, CpuMillis: unit.CPUMillis, MemoryMib: unit.MemoryMiB})
			if err != nil {
				return err
			}
			if reserved != 1 {
				return errors.New("recovery participant is reserved by another workspace")
			}
		}
	}
	var observedNetworkGeneration int64
	for server, host := range source.Hosts {
		// What a revoked host holds decides nothing; ignoreRevokedExports dropped its claims.
		if revoked, err := serverRevoked(ctx, tx, in.WorkspaceID, server); err != nil || revoked {
			if err != nil {
				return err
			}
			continue
		}
		expected, err := transferInventory(ctx, tx, in.WorkspaceID, server)
		if err != nil {
			return err
		}
		allowed := map[string][]string{}
		for _, root := range roots {
			for _, unit := range root.Body.Plan.Distributed.Units {
				if unit.Plan.ServerID != server {
					continue
				}
				values := []string{unit.Previous.Hash, unit.Plan.Hash}
				if root.Body.Maintenance == nil && !unit.Plan.Local.Primary && len(unit.Plan.LocalApplications()) == 0 {
					var complete bool
					// inline-sql: recovery session temporary table
					if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_input_proofs WHERE server_id=$1 AND parent_id=$2 AND id=$3 AND applied AND report->>'state'='succeeded')`, server, root.Command.Id, phaseKey(root.Command.Id, "complete", unit.Plan.UnitID)).Scan(&complete); err != nil {
						return err
					}
					if complete {
						values = append(values, "")
					}
				}
				if root.Body.Maintenance != nil {
					for _, m := range root.Body.Maintenance.Units {
						if m.Unit.Plan.ServerID == server && m.Intent.Target != nil {
							values = append(values, m.Intent.Target.Hash)
						}
					}
				}
				allowed[unit.Plan.UnitID.String()] = values
			}
		}
		if err = checkRecoveredInventory(expected, host, allowed); err != nil {
			return err
		}
		for _, manifest := range host.Networks {
			observedNetworkGeneration = max(observedNetworkGeneration, manifest.Generation)
			raw, _ := stablejson.Marshal(manifest)
			updated, err := q.AdoptRecoveredNetworkManifest(ctx, db.AdoptRecoveredNetworkManifestParams{InstalledManifest: raw, ManifestHash: hostnetwork.Hash(manifest), WorkspaceID: in.WorkspaceID, ServerID: server, MembershipID: manifest.MembershipID, NetworkID: manifest.NetworkID, PublicKey: manifest.PublicKey})
			if err != nil {
				return err
			}
			if updated != 1 {
				return errors.New("installed network membership is incompatible with the selected checkpoint")
			}
		}
	}
	if observedNetworkGeneration > 0 {
		generation, err := q.AdvanceNetworkGeneration(ctx, db.AdvanceNetworkGenerationParams{ObservedGeneration: observedNetworkGeneration, WorkspaceID: in.WorkspaceID})
		if err != nil {
			return err
		}
		if err := q.ResetRecoveredNetworkMembers(ctx, db.ResetRecoveredNetworkMembersParams{Generation: generation, WorkspaceID: in.WorkspaceID}); err != nil {
			return err
		}
	}
	if err := q.StampOperationControllerGenerations(ctx, in.WorkspaceID); err != nil {
		return err
	}
	if err := q.RaiseEnvironmentExecutionGenerations(ctx, in.WorkspaceID); err != nil {
		return err
	}
	if err := q.RaiseServerUpdateGenerations(ctx, in.WorkspaceID); err != nil {
		return err
	}
	// inline-sql: recovery session temporary table
	rows, err := tx.Query(ctx, `SELECT id FROM execution_operations WHERE workspace_id=$1 AND state IN ('queued','dispatched','running','needs_attention') AND (id IN (SELECT id FROM recovery_selected_roots) OR parent_id IN (SELECT id FROM recovery_selected_roots) OR id IN (SELECT id FROM recovery_standalone_restrictions)) ORDER BY created_at,id`, in.WorkspaceID)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		job, err := s.Queue.InsertTx(ctx, tx, jobArgs{ID: id}, &river.InsertOpts{MaxAttempts: 100})
		if err != nil {
			return err
		}
		if err = q.SetOperationJob(ctx, db.SetOperationJobParams{JobID: job.Job.ID, WorkspaceID: in.WorkspaceID, ID: id}); err != nil {
			return err
		}
	}
	return q.InsertAudit(ctx, db.InsertAuditParams{WorkspaceID: in.WorkspaceID, ID: uuid.New(), ActorID: in.OwnerID, Action: "workspace.recover_cross_host_checkpoint", ResourceID: in.WorkspaceID})
}

func checkRecoveredInventory(expected api.ControllerInventory, host deployment.RecoveryEvidence, allowed map[string][]string) error {
	for id, hash := range expected.Units {
		actual, exists := host.Units[id]
		if alternatives, recovered := allowed[id]; recovered {
			if !exists && containsRecoveryHash(alternatives, "") {
				continue
			}
			if !exists || !containsRecoveryHash(alternatives, actual) {
				return errors.New("a recovered unit has incompatible source or target inventory")
			}
		} else if !exists || actual != hash {
			return errors.New("another environment changed; provide its compatible recovery capsule")
		}
	}
	for id, hash := range host.Units {
		if _, exists := expected.Units[id]; exists {
			continue
		}
		if !containsRecoveryHash(allowed[id], hash) {
			return errors.New("host contains a unit missing from the selected recovery metadata")
		}
	}
	if len(expected.Schedules) != len(host.Schedules) {
		return errors.New("host schedule inventory differs from the selected recovery metadata")
	}
	for id, generation := range expected.Schedules {
		if actual, exists := host.Schedules[id]; !exists || actual != generation {
			return errors.New("host schedule generation differs from the selected recovery metadata")
		}
	}
	return nil
}

func containsRecoveryHash(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
