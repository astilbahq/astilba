package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func fenceRecoveryWorkspace(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	// inline-sql: recovery session temporary tables.
	if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state='cancelled',cancel_requested=true,job_id=NULL,note='Fenced by offline controller recovery.' WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled') AND id NOT IN (SELECT id FROM recovery_selected_roots) AND id NOT IN (SELECT id FROM recovery_standalone_restrictions) AND (parent_id IS NULL OR parent_id NOT IN (SELECT id FROM recovery_selected_roots))`, ws); err != nil {
		return err
	}
	// inline-sql: river_job is owned by River.
	if _, err := tx.Exec(ctx, `DELETE FROM river_job WHERE (kind='platform_execution_v1' AND args->>'id' IN (SELECT id::text FROM execution_operations WHERE workspace_id=$1)) OR (kind='platform_git_refresh_v1' AND args->>'workspace'=($1::uuid)::text) OR (kind='platform_automatic_deployment_v1' AND args->>'ID' IN (SELECT id::text FROM pipeline_auto_deployments WHERE workspace_id=$1)) OR (kind='platform_dns_v1' AND args->>'server' IN (SELECT id::text FROM servers WHERE workspace_id=$1)) OR (kind='platform_source_submission_v1' AND args->>'workspace'=($1::uuid)::text)`, ws); err != nil {
		return err
	}
	q := db.New(tx)
	blockAutoDeployments := func(ctx context.Context, ws uuid.UUID) error {
		return q.BlockWorkspaceAutoDeployments(ctx, db.BlockWorkspaceAutoDeploymentsParams{Note: "Offline recovery requires renewed approval.", WorkspaceID: ws})
	}
	for _, fence := range []func(context.Context, uuid.UUID) error{
		q.ClearWorkspaceOperationJobs,
		q.RequireWorkspaceGitApproval,
		q.FenceWorkspaceSubmissions,
		q.DisableWorkspaceBuildAutomation,
		q.DisableWorkspacePreviewPolicies,
		q.BlockWorkspacePreviewRequests,
		q.RevokeWorkspaceAutomationCredentials,
		blockAutoDeployments,
		q.FenceWorkspaceBackupPolicies,
		q.ResetWorkspaceDomainApproval,
	} {
		if err := fence(ctx, ws); err != nil {
			return err
		}
	}
	// inline-sql: recovery session temporary table.
	if _, err := tx.Exec(ctx, `UPDATE network_endpoint_sources s SET active=false WHERE workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM recovery_incoming_grants g WHERE g.endpoint_id=s.endpoint_id AND g.server_id=s.server_id AND g.unit_id=s.unit_id)`, ws); err != nil {
		return err
	}
	return store.FenceTroubleshooting(ctx, q, ws)
}

func (s *Service) restoreRecoveryRoots(ctx context.Context, tx pgx.Tx, ws uuid.UUID, roots map[uuid.UUID]recoveringRoot, sourceHash, inputHash string, owner uuid.UUID) error {
	q := db.New(tx)
	for parent, root := range roots {
		descriptor, _ := stablejson.Marshal(root.Descriptor)
		if err := q.RestoreRecoveryRoot(ctx, db.RestoreRecoveryRootParams{Descriptor: descriptor, WorkspaceID: ws, ID: parent}); err != nil {
			return err
		}
		recorded, err := q.UpsertCrossHostRecovery(ctx, db.UpsertCrossHostRecoveryParams{SourceHash: sourceHash, InputHash: inputHash, OwnerID: owner, WorkspaceID: ws, ParentID: parent})
		if err != nil {
			return err
		}
		if recorded != 1 {
			return errors.New("recovery root does not belong to this workspace")
		}
		for sequence := int64(0); sequence <= root.Descriptor.Sequence; sequence++ {
			var descriptor, envelope, decoded []byte
			var retained bool
			// inline-sql: recovery session temporary table.
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
					if err := q.SetCrossHostOperationPhase(ctx, db.SetCrossHostOperationPhaseParams{Phase: phase, WorkspaceID: ws, ID: parent}); err != nil {
						return err
					}
				}
			}
			existing, err := q.RecoveryCheckpointEnvelope(ctx, db.RecoveryCheckpointEnvelopeParams{WorkspaceID: ws, ID: d.CheckpointID})
			if err == nil && deployment.RecoveryDigest(existing) != d.Digest {
				return errors.New("checkpoint history conflicts with the host capsule")
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if err = q.RestoreRecoveryCheckpoint(ctx, db.RestoreRecoveryCheckpointParams{ID: d.CheckpointID, WorkspaceID: ws, ParentID: parent, Sequence: sequence, Descriptor: descriptor, Envelope: envelope, Phase: phase, Acknowledged: retained}); err != nil {
					return err
				}
			} else {
				if err = q.SetRecoveryCheckpointAcknowledged(ctx, db.SetRecoveryCheckpointAcknowledgedParams{Acknowledged: retained, WorkspaceID: ws, ID: d.CheckpointID}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) restoreRecoverySegment(ctx context.Context, tx pgx.Tx, ws uuid.UUID, root recoveringRoot, d deployment.RecoveryDescriptor, segment recoveryContinuation) error {
	q := db.New(tx)
	if err := q.MergeOperationCancelRequest(ctx, db.MergeOperationCancelRequestParams{CancelRequested: segment.CancelRequested, WorkspaceID: ws, ID: root.Command.Id}); err != nil {
		return err
	}
	if len(segment.UpgradeStatus) > 0 && d.Sequence > root.KnownSequence {
		if root.Body.Maintenance == nil || root.Body.Maintenance.Effect.Upgrade == nil {
			return errors.New("upgrade status has no frozen upgrade")
		}
		if err := q.SaveSupportedUpgradeStatus(ctx, db.SaveSupportedUpgradeStatusParams{Status: segment.UpgradeStatus, WorkspaceID: ws, ID: root.Body.Maintenance.Effect.Upgrade.Plan.ID}); err != nil {
			return err
		}
	}
	for _, approval := range segment.Approvals {
		if err := restoreRecoveryApproval(ctx, q, ws, root.Command.Id, approval); err != nil {
			return err
		}
	}
	for _, child := range segment.Children {
		c, err := recoveryChildFrame(root.Command, root.Body, segment.Phase, child.Header)
		if err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(d)
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

// recoveryApproval is a checkpoint's copy of a cross_host_approvals row.
type recoveryApproval struct {
	WorkspaceID   uuid.UUID `json:"workspace_id"`
	RequestKey    uuid.UUID `json:"request_key"`
	ParentID      uuid.UUID `json:"parent_id"`
	UpgradeID     uuid.UUID `json:"upgrade_id"`
	Action        string    `json:"action"`
	ExpectedPhase string    `json:"expected_phase"`
}

// decodeRecoveryApproval accepts an approval only for the root being restored, in
// the workspace being recovered; the checkpoint cannot name any other.
func decodeRecoveryApproval(ws, parent uuid.UUID, approval portability.Row) (recoveryApproval, []byte, error) {
	raw, err := stablejson.Marshal(approval)
	if err != nil {
		return recoveryApproval{}, nil, err
	}
	var a recoveryApproval
	if json.Unmarshal(raw, &a) != nil || a.RequestKey == uuid.Nil {
		return a, nil, errors.New("invalid recovered owner approval")
	}
	if a.WorkspaceID != ws || a.ParentID != parent {
		return a, nil, errors.New("recovered owner approval belongs to another workspace or root")
	}
	return a, raw, nil
}

func restoreRecoveryApproval(ctx context.Context, q *db.Queries, ws, parent uuid.UUID, approval portability.Row) error {
	a, raw, err := decodeRecoveryApproval(ws, parent, approval)
	if err != nil {
		return err
	}
	matches, err := q.RecoveryApprovalMatches(ctx, db.RecoveryApprovalMatchesParams{Approval: raw, WorkspaceID: ws, RequestKey: a.RequestKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return q.RestoreCrossHostApproval(ctx, db.RestoreCrossHostApprovalParams{WorkspaceID: ws, RequestKey: a.RequestKey, ParentID: parent, UpgradeID: a.UpgradeID, Action: a.Action, ExpectedPhase: a.ExpectedPhase})
	}
	if err == nil && !matches {
		return errors.New("owner approval identity conflicts with existing history")
	}
	return err
}

func storeRecoveryFrame(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	return storeRecoveryFrameState(ctx, tx, ws, c, "queued")
}

func storeRecoveryFrameState(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, initial api.ExecutionState) error {
	q := db.New(tx)
	frame, err := q.RecoveryFrame(ctx, db.RecoveryFrameParams{WorkspaceID: ws, ID: c.Id})
	if err == nil {
		var prior api.ExecutionCommand
		if json.Unmarshal(frame.Command, &prior) != nil || (prior.ParentId == nil) != (c.ParentId == nil) || prior.ParentId != nil && *prior.ParentId != *c.ParentId || prior.ServerId != c.ServerId || prior.EnvironmentId != c.EnvironmentId || prior.ResourceId != c.ResourceId || prior.Kind != c.Kind || prior.Action != c.Action || prior.Generation != c.Generation || prior.PlanHash != c.PlanHash || !prior.Deadline.Equal(c.Deadline) {
			return errors.New("frozen child header conflicts with current history")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		raw, _ := stablejson.Marshal(c)
		var env *uuid.UUID
		if c.EnvironmentId != uuid.Nil {
			env = &c.EnvironmentId
		}
		return q.InsertRecoveryFrame(ctx, db.InsertRecoveryFrameParams{ID: c.Id, WorkspaceID: ws, ServerID: c.ServerId, EnvironmentID: env, Generation: c.Generation, RequestKey: uuid.New(), Command: raw, Kind: string(c.Kind), State: string(initial), ParentID: c.ParentId})
	}
	// The frame matches its frozen header, so it keeps its stored bytes and gets only the
	// checkpoint it now belongs to.
	var descriptor []byte
	if c.RecoveryCheckpoint != nil {
		descriptor, _ = stablejson.Marshal(c.RecoveryCheckpoint)
	}
	if frame.ReportSequence > 0 {
		return q.SetRecoveryFrameCheckpoint(ctx, db.SetRecoveryFrameCheckpointParams{Descriptor: descriptor, WorkspaceID: ws, ID: c.Id})
	}
	return q.ResetRecoveryFrame(ctx, db.ResetRecoveryFrameParams{Descriptor: descriptor, State: string(initial), WorkspaceID: ws, ID: c.Id})
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
