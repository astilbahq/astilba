package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func fenceRecoveryWorkspace(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	statements := []string{
		`UPDATE execution_operations SET state='cancelled',cancel_requested=true,job_id=NULL,note='Fenced by offline controller recovery.' WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled') AND id NOT IN (SELECT id FROM recovery_selected_roots) AND id NOT IN (SELECT id FROM recovery_standalone_restrictions) AND (parent_id IS NULL OR parent_id NOT IN (SELECT id FROM recovery_selected_roots))`,
		`DELETE FROM river_job WHERE (kind='platform_execution_v1' AND args->>'id' IN (SELECT id::text FROM execution_operations WHERE workspace_id=$1)) OR (kind='platform_git_refresh_v1' AND args->>'workspace'=($1::uuid)::text) OR (kind='platform_automatic_deployment_v1' AND args->>'ID' IN (SELECT id::text FROM pipeline_auto_deployments WHERE workspace_id=$1)) OR (kind='platform_dns_v1' AND args->>'server' IN (SELECT id::text FROM servers WHERE workspace_id=$1))`,
		`UPDATE execution_operations SET job_id=NULL WHERE workspace_id=$1`,
		`UPDATE git_sources SET configuration=configuration || '{"autoDeploy":false}'::jsonb,status='approval_required' WHERE workspace_id=$1`,
		`UPDATE build_targets SET configuration=configuration || '{"autoBuild":false,"autoDeploy":false}'::jsonb WHERE workspace_id=$1`,
		`UPDATE preview_policies SET enabled=false WHERE workspace_id=$1`,
		`UPDATE preview_requests SET state=CASE WHEN state='deleted' THEN state ELSE 'blocked' END,resume_after_cancel=false,cleanup_requested=false,cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL,note='Offline recovery requires renewed preview approval.' WHERE workspace_id=$1`,
		`UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE workspace_id=$1`,
		`UPDATE pipeline_auto_deployments SET state='blocked',note='Offline recovery requires renewed approval.' WHERE workspace_id=$1`,
		`UPDATE backup_policies SET configuration=jsonb_set(configuration,'{enabled}','false'),coordinated_checked_at=clock_timestamp() WHERE workspace_id=$1`,
		`UPDATE server_domains SET approved_controller=NULL,state='review_required',note='Offline recovery requires renewed domain approval.' WHERE workspace_id=$1`,
		`UPDATE network_endpoint_sources s SET active=false WHERE workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM recovery_incoming_grants g WHERE g.endpoint_id=s.endpoint_id AND g.server_id=s.server_id AND g.unit_id=s.unit_id)`,
	}
	for _, query := range statements {
		if _, err := tx.Exec(ctx, query, ws); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreRecoveryRoots(ctx context.Context, tx pgx.Tx, ws uuid.UUID, roots map[uuid.UUID]recoveringRoot, sourceHash, inputHash string, owner uuid.UUID) error {
	for parent, root := range roots {
		raw, _ := json.Marshal(root.Command)
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET command=$3,state='running',job_id=NULL WHERE workspace_id=$1 AND id=$2`, ws, parent, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cross_host_recoveries(parent_id,workspace_id,opened,source_hash,input_hash,owner_id) VALUES($1,$2,false,$3,$4,$5) ON CONFLICT(parent_id) DO UPDATE SET opened=false,source_hash=excluded.source_hash,input_hash=excluded.input_hash,owner_id=excluded.owner_id`, parent, ws, sourceHash, inputHash, owner); err != nil {
			return err
		}
		for sequence := int64(0); sequence <= root.Descriptor.Sequence; sequence++ {
			var descriptor, envelope, decoded []byte
			var retained bool
			if err := tx.QueryRow(ctx, `SELECT descriptor,envelope,decoded,retained FROM recovery_input_checkpoints WHERE parent_id=$1 AND sequence=$2`, parent, sequence).Scan(&descriptor, &envelope, &decoded, &retained); err != nil {
				return err
			}
			var d deployment.RecoveryDescriptor
			if json.Unmarshal(descriptor, &d) != nil {
				return errors.New("invalid stored recovery descriptor")
			}
			phase := "base"
			if sequence > 0 {
				var segment recoveryContinuation
				if recoveryDecode(decoded, &segment) != nil {
					return errors.New("invalid stored recovery continuation")
				}
				phase = segment.Phase
				if err := s.restoreRecoverySegment(ctx, tx, ws, root, d, segment); err != nil {
					return err
				}
				if d.Sequence > root.KnownSequence {
					if _, err := tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, parent, phase); err != nil {
						return err
					}
				}
			}
			var existing []byte
			err := tx.QueryRow(ctx, `SELECT envelope FROM cross_host_checkpoints WHERE id=$1`, d.CheckpointID).Scan(&existing)
			if err == nil && deployment.RecoveryDigest(existing) != d.Digest {
				return errors.New("checkpoint history conflicts with the host capsule")
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err = tx.Exec(ctx, `INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope,phase,acknowledged) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, d.CheckpointID, ws, parent, sequence, descriptor, envelope, phase, retained); err != nil {
					return err
				}
			} else {
				if _, err = tx.Exec(ctx, `UPDATE cross_host_checkpoints SET acknowledged=$2 WHERE id=$1`, d.CheckpointID, retained); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) restoreRecoverySegment(ctx context.Context, tx pgx.Tx, ws uuid.UUID, root recoveringRoot, d deployment.RecoveryDescriptor, segment recoveryContinuation) error {
	if _, err := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=cancel_requested OR $3 WHERE workspace_id=$1 AND id=$2`, ws, root.Command.Id, segment.CancelRequested); err != nil {
		return err
	}
	if len(segment.UpgradeStatus) > 0 && d.Sequence > root.KnownSequence {
		if root.Body.Maintenance == nil || root.Body.Maintenance.Effect.Upgrade == nil {
			return errors.New("upgrade status has no frozen upgrade")
		}
		if _, err := tx.Exec(ctx, `UPDATE supported_upgrades SET status=$3 WHERE workspace_id=$1 AND id=$2`, ws, root.Body.Maintenance.Effect.Upgrade.Plan.ID, []byte(segment.UpgradeStatus)); err != nil {
			return err
		}
	}
	for _, approval := range segment.Approvals {
		raw, _ := json.Marshal(approval)
		var matches bool
		err := tx.QueryRow(ctx, `SELECT to_jsonb(a)=$2::jsonb FROM cross_host_approvals a WHERE workspace_id=$1 AND request_key=($2::jsonb->>'request_key')::uuid`, ws, raw).Scan(&matches)
		if err == nil && !matches {
			return errors.New("owner approval identity conflicts with existing history")
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err = tx.Exec(ctx, `INSERT INTO cross_host_approvals SELECT * FROM jsonb_populate_record(NULL::cross_host_approvals,$1)`, raw); err != nil {
				return err
			}
		}
	}
	for _, child := range segment.Children {
		c, err := recoveryChildFrame(root.Command, root.Body, segment.Phase, child.Header)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(d)
		c.RecoveryCheckpoint = rawMessage(raw)
		if err = storeRecoveryFrame(ctx, tx, ws, c); err != nil {
			return err
		}
		if err = s.applyRecoveryProof(ctx, tx, ws, c, root); err != nil {
			return err
		}
	}
	return nil
}

func storeRecoveryFrame(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	return storeRecoveryFrameState(ctx, tx, ws, c, "queued")
}

func storeRecoveryFrameState(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, initial api.ExecutionState) error {
	var existing []byte
	var reported int64
	err := tx.QueryRow(ctx, `SELECT command,report_sequence FROM execution_operations WHERE workspace_id=$1 AND id=$2`, ws, c.Id).Scan(&existing, &reported)
	if err == nil {
		var prior api.ExecutionCommand
		if json.Unmarshal(existing, &prior) != nil || (prior.ParentId == nil) != (c.ParentId == nil) || prior.ParentId != nil && *prior.ParentId != *c.ParentId || prior.ServerId != c.ServerId || prior.EnvironmentId != c.EnvironmentId || prior.ResourceId != c.ResourceId || prior.Kind != c.Kind || prior.Action != c.Action || prior.Generation != c.Generation || prior.PlanHash != c.PlanHash || !prior.Deadline.Equal(c.Deadline) {
			return errors.New("frozen child header conflicts with current history")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	raw, _ := json.Marshal(c)
	if errors.Is(err, pgx.ErrNoRows) {
		var env *uuid.UUID
		if c.EnvironmentId != uuid.Nil {
			env = &c.EnvironmentId
		}
		_, err = tx.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,parent_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$10,$9)`, c.Id, ws, c.ServerId, env, c.Generation, uuid.New(), raw, c.Kind, c.ParentId, initial)
	} else if reported > 0 {
		_, err = tx.Exec(ctx, `UPDATE execution_operations SET command=$2,job_id=NULL WHERE id=$1`, c.Id, raw)
	} else {
		_, err = tx.Exec(ctx, `UPDATE execution_operations SET command=$2,state=$3,report_sequence=0,steps='[]',note='',job_id=NULL WHERE id=$1`, c.Id, raw, initial)
	}
	return err
}

func metadataRecoveryPhase(root recoveringRoot, c api.ExecutionCommand) string {
	if root.Body.Maintenance != nil {
		return "maintenance-checkpoint"
	}
	return "checkpoint"
}

func isRecoveryMetadataFrame(root recoveringRoot, c api.ExecutionCommand) bool {
	if c.RecoveryCheckpoint == nil {
		return false
	}
	var d deployment.RecoveryDescriptor
	if json.Unmarshal(*c.RecoveryCheckpoint, &d) != nil || !deployment.SameRecoveryRoot(d, root.Descriptor) {
		return false
	}
	return c.Id == phaseKey(root.Command.Id, metadataRecoveryPhase(root, c), root.Body.Plan.UnitID) || c.Id == uuid.NewSHA1(d.CheckpointID, []byte("recovery-delivery"))
}
