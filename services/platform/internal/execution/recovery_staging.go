package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

func (s *Service) finishRecoveryStaging(ctx context.Context, tx pgx.Tx, in RecoveryImport, source recoverySource, roots map[uuid.UUID]recoveringRoot) error {
	if s.Queue == nil {
		return errors.New("offline recovery requires the configured durable queue")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cross_host_reservations WHERE workspace_id=$1 AND parent_id NOT IN (SELECT id FROM recovery_selected_roots)`, in.WorkspaceID); err != nil {
		return err
	}
	for parent, root := range roots {
		units := root.Body.Plan.Distributed.Units
		if root.Body.Maintenance != nil {
			units = root.Body.Plan.ActiveUnits()
		}
		for _, unit := range units {
			if _, err := tx.Exec(ctx, `INSERT INTO cross_host_reservations(server_id,workspace_id,parent_id,cpu_millis,memory_mib) VALUES($1,$2,$3,$4,$5) ON CONFLICT(server_id) DO UPDATE SET parent_id=excluded.parent_id,cpu_millis=excluded.cpu_millis,memory_mib=excluded.memory_mib WHERE cross_host_reservations.workspace_id=excluded.workspace_id`, unit.Plan.ServerID, in.WorkspaceID, parent, unit.CPUMillis, unit.MemoryMiB); err != nil {
				return err
			}
		}
	}
	var observedNetworkGeneration int64
	for server, host := range source.Hosts {
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
			raw, _ := json.Marshal(manifest)
			updated, err := tx.Exec(ctx, `UPDATE network_members SET installed_manifest=$4,manifest_hash=$5,acknowledged_generation=0,note='Controller recovery preserves the installed private links; general network mutation requires renewed approval.' WHERE workspace_id=$1 AND server_id=$2 AND membership_id=$3 AND network_id=$6 AND public_key=$7`, in.WorkspaceID, server, manifest.MembershipID, raw, hostnetwork.Hash(manifest), manifest.NetworkID, manifest.PublicKey)
			if err != nil {
				return err
			}
			if updated.RowsAffected() != 1 {
				return errors.New("installed network membership is incompatible with the selected checkpoint")
			}
		}
	}
	if observedNetworkGeneration > 0 {
		var generation int64
		if err := tx.QueryRow(ctx, `UPDATE workspace_networks SET generation=GREATEST(generation,$2)+1 WHERE workspace_id=$1 RETURNING generation`, in.WorkspaceID, observedNetworkGeneration).Scan(&generation); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE network_members SET generation=$2,acknowledged_generation=0 WHERE workspace_id=$1`, in.WorkspaceID, generation); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE execution_operations o SET command=jsonb_set(command,'{controllerGeneration}',to_jsonb(s.controller_generation),true) FROM servers s WHERE o.workspace_id=$1 AND s.workspace_id=o.workspace_id AND s.id=o.server_id AND COALESCE(o.command->'crosshost'->>'phase','')<>'recovery-open'`, in.WorkspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE environments e SET execution_generation=GREATEST(execution_generation,COALESCE((SELECT max(generation) FROM execution_operations o WHERE o.workspace_id=e.workspace_id AND o.environment_id=e.id AND kind<>'network'),0)) WHERE workspace_id=$1`, in.WorkspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE servers s SET update_generation=GREATEST(update_generation,COALESCE((SELECT max(generation) FROM execution_operations o WHERE o.server_id=s.id AND o.kind IN ('network','update')),0)),last_seen=NULL WHERE workspace_id=$1`, in.WorkspaceID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM execution_operations WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled') AND (id IN (SELECT id FROM recovery_selected_roots) OR parent_id IN (SELECT id FROM recovery_selected_roots) OR id IN (SELECT id FROM recovery_standalone_restrictions)) ORDER BY created_at,id`, in.WorkspaceID)
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
		if _, err = tx.Exec(ctx, `UPDATE execution_operations SET job_id=$2 WHERE id=$1`, id, job.Job.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,id,actor_id,action,resource_id) VALUES($1,$2,$3,'workspace.recover_cross_host_checkpoint',$1)`, in.WorkspaceID, uuid.New(), in.OwnerID)
	return err
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
