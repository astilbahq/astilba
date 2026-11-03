-- Controller fencing and offline recovery. Statements over the recovery session's
-- temporary tables stay inline in internal/execution; they cannot be typed here.

-- name: RecoveryServerWorkspace :one
-- unscoped: the offline operator command names the server, which establishes the workspace.
SELECT workspace_id FROM servers WHERE id = @server_id;

-- name: LockServerControllerGeneration :one
SELECT controller_generation FROM servers WHERE workspace_id = @workspace_id AND id = @server_id FOR UPDATE;

-- name: ServerHasReservation :one
SELECT EXISTS(SELECT 1 FROM cross_host_reservations WHERE workspace_id = @workspace_id AND server_id = @server_id);

-- name: FenceServerController :exec
-- Moves the server to a new controller generation and clears every credential,
-- so nothing issued before recovery can act on it again.
UPDATE servers SET controller_generation = @controller_generation,awaiting_transfer=true,credential_hash=NULL,
    previous_hash=NULL,previous_until=NULL,enrollment_hash=NULL,enrollment_expires=NULL,poll_id=NULL,poll_until=NULL,rotation_requested=false
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: CancelServerOperationsForRecovery :exec
UPDATE execution_operations SET state='cancelled',cancel_requested=true,note='Fenced after management recovery; inspect the host before resuming.'
WHERE workspace_id = @workspace_id AND server_id = @server_id AND state NOT IN ('succeeded','failed','cancelled');

-- name: DisableWorkspaceGitAutoDeploy :exec
UPDATE git_sources SET configuration=configuration || '{"autoDeploy":false}'::jsonb WHERE workspace_id = @workspace_id;

-- name: RequireWorkspaceGitApproval :exec
UPDATE git_sources SET configuration=configuration || '{"autoDeploy":false}'::jsonb,status='approval_required' WHERE workspace_id = @workspace_id;

-- name: DisableWorkspaceBuildAutomation :exec
UPDATE build_targets SET configuration=configuration || '{"autoBuild":false,"autoDeploy":false}'::jsonb WHERE workspace_id = @workspace_id;

-- name: DisableServerPreviewPolicies :exec
UPDATE preview_policies SET enabled=false WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: BlockServerPreviewRequests :exec
UPDATE preview_requests SET state=CASE WHEN state='deleted' THEN state ELSE 'blocked' END,resume_after_cancel=false,
    cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL,note='Management recovery requires renewed preview approval.'
WHERE preview_requests.workspace_id = @workspace_id AND preview_requests.policy_id IN (
    SELECT p.id FROM preview_policies p WHERE p.workspace_id = @workspace_id AND p.server_id = @server_id);

-- name: DisableWorkspacePreviewPolicies :exec
UPDATE preview_policies SET enabled=false WHERE workspace_id = @workspace_id;

-- name: BlockWorkspacePreviewRequests :exec
UPDATE preview_requests SET state=CASE WHEN state='deleted' THEN state ELSE 'blocked' END,resume_after_cancel=false,cleanup_requested=false,
    cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL,note='Offline recovery requires renewed preview approval.'
WHERE workspace_id = @workspace_id;

-- name: RevokeWorkspaceAutomationCredentials :exec
UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE workspace_id = @workspace_id;

-- name: BlockWorkspaceAutoDeployments :exec
UPDATE pipeline_auto_deployments SET state='blocked',note = @note WHERE workspace_id = @workspace_id;

-- name: ResetServerDomainApproval :exec
UPDATE server_domains SET approved_controller=NULL,state='review_required',note='Management recovery requires renewed approval.'
WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: ResetWorkspaceDomainApproval :exec
UPDATE server_domains SET approved_controller=NULL,state='review_required',note='Offline recovery requires renewed domain approval.'
WHERE workspace_id = @workspace_id;

-- name: ClearWorkspaceOperationJobs :exec
UPDATE execution_operations SET job_id=NULL WHERE workspace_id = @workspace_id;

-- name: FenceWorkspaceBackupPolicies :exec
UPDATE backup_policies SET configuration=jsonb_set(configuration,'{enabled}','false'),coordinated_checked_at=clock_timestamp()
WHERE workspace_id = @workspace_id;

-- name: RestoreRecoveryRoot :exec
UPDATE execution_operations SET command = @command,state='running',job_id=NULL
WHERE workspace_id = @workspace_id AND id = @id;

-- name: UpsertCrossHostRecovery :execrows
-- The parent's foreign key and the conflict target ignore the workspace, so the
-- row comes from the workspace's own operation; no row is written for any other.
INSERT INTO cross_host_recoveries(parent_id,workspace_id,opened,source_hash,input_hash,owner_id)
SELECT h.id,h.workspace_id,false,@source_hash::text,@input_hash::text,@owner_id::uuid
FROM cross_host_operations h WHERE h.workspace_id = @workspace_id AND h.id = @parent_id
ON CONFLICT(parent_id) DO UPDATE SET opened=false,source_hash=excluded.source_hash,input_hash=excluded.input_hash,owner_id=excluded.owner_id;

-- name: SetCrossHostOperationPhase :exec
UPDATE cross_host_operations SET phase = @phase WHERE workspace_id = @workspace_id AND id = @id;

-- name: CrossHostOperationPhase :one
SELECT phase FROM cross_host_operations WHERE workspace_id = @workspace_id AND id = @id;

-- name: RecoveryCheckpointEnvelope :one
SELECT envelope FROM cross_host_checkpoints WHERE workspace_id = @workspace_id AND id = @id;

-- name: RecoveryCheckpointPhase :one
SELECT phase FROM cross_host_checkpoints WHERE workspace_id = @workspace_id AND id = @id AND parent_id = @parent_id;

-- name: RecoveryCheckpointAt :one
SELECT descriptor,envelope,phase,acknowledged FROM cross_host_checkpoints
WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND sequence = @sequence;

-- name: InsertRecoveryBaseCheckpoint :exec
INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope)
VALUES(@id,@workspace_id,@parent_id,0,@descriptor,@envelope);

-- name: InsertRecoveryCheckpoint :exec
INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope,phase)
VALUES(@id,@workspace_id,@parent_id,@sequence,@descriptor,@envelope,@phase);

-- name: RestoreRecoveryCheckpoint :exec
INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope,phase,acknowledged)
VALUES(@id,@workspace_id,@parent_id,@sequence,@descriptor,@envelope,@phase,@acknowledged);

-- name: SetRecoveryCheckpointAcknowledged :exec
UPDATE cross_host_checkpoints SET acknowledged = @acknowledged WHERE workspace_id = @workspace_id AND id = @id;

-- name: AgentRecoveryCheckpointEnvelope :one
-- Returned only to the root's assigned primary, on a credential that is still valid.
SELECT c.envelope FROM cross_host_checkpoints c
JOIN execution_operations o ON o.workspace_id=c.workspace_id AND o.id=c.parent_id
JOIN servers s ON s.id=o.server_id AND s.workspace_id=o.workspace_id
WHERE c.workspace_id = @workspace_id AND c.parent_id = @parent_id AND c.id = @id AND o.parent_id IS NULL AND o.server_id = @server_id
    AND NOT s.revoked AND NOT s.awaiting_transfer
    AND (s.credential_hash = @credential_hash OR (s.previous_hash = @credential_hash AND s.previous_until>now()));

-- name: OperationCancelRequested :one
SELECT cancel_requested FROM execution_operations WHERE workspace_id = @workspace_id AND id = @id;

-- name: OperationExists :one
SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id = @workspace_id AND id = @id);

-- name: RecoveryChildCommand :one
SELECT command FROM execution_operations WHERE workspace_id = @workspace_id AND id = @id AND parent_id = @parent_id::uuid;

-- name: AttachRecoveryCheckpoint :exec
UPDATE execution_operations SET command=jsonb_set(command,'{recoveryCheckpoint}',@descriptor::jsonb,true)
WHERE workspace_id = @workspace_id AND (id = @parent_id OR id = ANY(@child_ids::uuid[]));

-- name: RecoveryPhaseApprovals :many
SELECT to_jsonb(a)::jsonb AS approval FROM cross_host_approvals a
WHERE a.workspace_id = @workspace_id AND a.parent_id = @parent_id AND @phase::text LIKE '%:'||a.request_key::text;

-- name: RecoveryApprovalMatches :one
SELECT (to_jsonb(a) = @approval::jsonb)::boolean AS matches FROM cross_host_approvals a
WHERE a.workspace_id = @workspace_id AND a.request_key = @request_key;

-- name: RestoreCrossHostApproval :exec
INSERT INTO cross_host_approvals(workspace_id,request_key,parent_id,upgrade_id,action,expected_phase)
VALUES(@workspace_id,@request_key,@parent_id,@upgrade_id,@action,@expected_phase);

-- name: SupportedUpgradeStatus :one
SELECT status FROM supported_upgrades WHERE workspace_id = @workspace_id AND id = @id;

-- name: SaveSupportedUpgradeStatus :exec
UPDATE supported_upgrades SET status = @status WHERE workspace_id = @workspace_id AND id = @id;

-- name: MergeOperationCancelRequest :exec
UPDATE execution_operations SET cancel_requested=cancel_requested OR @cancel_requested::boolean
WHERE workspace_id = @workspace_id AND id = @id;

-- name: RecoveryFrame :one
SELECT command,report_sequence FROM execution_operations WHERE workspace_id = @workspace_id AND id = @id;

-- name: InsertRecoveryFrame :exec
INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,parent_id)
VALUES(@id,@workspace_id,@server_id,@environment_id,@generation,@request_key, @command, @kind,@state,@parent_id);

-- name: SetRecoveryFrameCommand :exec
UPDATE execution_operations SET command = @command,job_id=NULL WHERE workspace_id = @workspace_id AND id = @id;

-- name: ResetRecoveryFrame :exec
UPDATE execution_operations SET command = @command,state = @state,report_sequence=0,steps='[]',note='',job_id=NULL
WHERE workspace_id = @workspace_id AND id = @id;

-- name: OperationReportState :one
SELECT report_sequence,state FROM execution_operations WHERE workspace_id = @workspace_id AND id = @id;

-- name: ApplyRecoveryProof :exec
UPDATE execution_operations SET state = @state,report_sequence = @report_sequence,steps = @steps,note = @note,
    cancel_requested=cancel_requested OR @cancel_requested::boolean
WHERE workspace_id = @workspace_id AND id = @id;

-- name: RestoreStandaloneRestriction :exec
-- Source is the operation's row as it appeared in the recovered archive.
UPDATE execution_operations SET state = @state,report_sequence=(@source::jsonb->>'report_sequence')::bigint,
    cancel_requested=(@source::jsonb->>'cancel_requested')::boolean,steps = @source::jsonb->'steps',note = @source::jsonb->>'note',job_id=NULL
WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockCrossHostRecoveryOpened :one
SELECT opened FROM cross_host_recoveries WHERE workspace_id = @workspace_id AND parent_id = @parent_id FOR UPDATE;

-- name: CountProvenRecoveryServers :one
SELECT count(*) FROM cross_host_recovery_servers r
JOIN servers s ON s.workspace_id=r.workspace_id AND s.id=r.server_id
WHERE r.workspace_id = @workspace_id AND r.parent_id = @parent_id AND r.verified
    AND NOT s.awaiting_transfer AND NOT s.revoked AND s.machine_id=r.machine_id AND s.controller_generation=r.controller_generation;

-- name: HoldRecoveredOperation :exec
UPDATE execution_operations SET state='needs_attention',note = @note WHERE workspace_id = @workspace_id AND id = @id;

-- name: VerifiedRecoveryController :one
SELECT controller_generation FROM cross_host_recovery_servers
WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND server_id = @server_id AND verified;

-- name: RecoveryOpenState :one
SELECT state FROM execution_operations
WHERE workspace_id = @workspace_id AND id = @id AND parent_id = @parent_id::uuid AND server_id = @server_id;

-- name: OpenCrossHostRecovery :exec
UPDATE cross_host_recoveries SET opened=true WHERE workspace_id = @workspace_id AND parent_id = @parent_id;

-- name: CrossHostRecoveryHeld :one
SELECT EXISTS(SELECT 1 FROM cross_host_recoveries WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND NOT opened);

-- name: CrossHostRecoveryApplied :one
SELECT EXISTS(SELECT 1 FROM cross_host_recoveries
    WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND source_hash = @source_hash AND input_hash = @input_hash AND owner_id = @owner_id);

-- name: RecoveryServerHandover :one
SELECT r.handover,s.controller_generation FROM cross_host_recovery_servers r
JOIN servers s ON s.workspace_id=r.workspace_id AND s.id=r.server_id
WHERE r.workspace_id = @workspace_id AND r.parent_id = @parent_id AND r.server_id = @server_id;

-- name: StageRecoveryServer :execrows
-- The conflict target ignores the workspace, so the update constrains it here.
INSERT INTO cross_host_recovery_servers(parent_id,workspace_id,server_id,machine_id,controller_generation,inventory,handover)
VALUES(@parent_id,@workspace_id,@server_id,@machine_id,@controller_generation,@inventory,@handover)
ON CONFLICT(parent_id,server_id) DO UPDATE SET machine_id=excluded.machine_id,controller_generation=excluded.controller_generation,
    inventory=excluded.inventory,handover=excluded.handover,verified=false
WHERE cross_host_recovery_servers.workspace_id=excluded.workspace_id;

-- name: WorkspaceOwnerExists :one
SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id = @workspace_id AND account_id = @account_id AND role='owner');

-- name: RemovedRuntimeCommand :one
SELECT o.command FROM environments e
JOIN execution_operations o ON o.workspace_id=e.workspace_id AND o.environment_id=e.id AND o.id=e.runtime_operation_id
WHERE e.workspace_id = @workspace_id AND e.id = @environment_id AND e.runtime_operation_id = @operation_id AND o.runtime_outcome='removed';

-- name: ReserveRecoveredServer :execrows
INSERT INTO cross_host_reservations(server_id,workspace_id,parent_id,cpu_millis,memory_mib)
VALUES(@server_id,@workspace_id,@parent_id,@cpu_millis,@memory_mib)
ON CONFLICT(server_id) DO UPDATE SET parent_id=excluded.parent_id,cpu_millis=excluded.cpu_millis,memory_mib=excluded.memory_mib
WHERE cross_host_reservations.workspace_id=excluded.workspace_id;

-- name: AdoptRecoveredNetworkManifest :execrows
UPDATE network_members SET installed_manifest = @installed_manifest,manifest_hash = @manifest_hash,acknowledged_generation=0,
    note='Controller recovery preserves the installed private links; general network mutation requires renewed approval.'
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND network_id = @network_id AND public_key = @public_key;

-- name: AdvanceRecoveredNetworkGeneration :one
UPDATE workspace_networks SET generation=GREATEST(generation, @observed_generation::bigint)+1
WHERE workspace_id = @workspace_id RETURNING generation;

-- name: ResetRecoveredNetworkMembers :exec
UPDATE network_members SET generation = @generation,acknowledged_generation=0 WHERE workspace_id = @workspace_id;

-- name: StampOperationControllerGenerations :exec
UPDATE execution_operations o SET command=jsonb_set(o.command,'{controllerGeneration}',to_jsonb(s.controller_generation),true)
FROM servers s
WHERE o.workspace_id = @workspace_id AND s.workspace_id=o.workspace_id AND s.id=o.server_id
    AND COALESCE(o.command->'crosshost'->>'phase','')<>'recovery-open';

-- name: RaiseEnvironmentExecutionGenerations :exec
UPDATE environments e SET execution_generation=GREATEST(e.execution_generation,COALESCE((
    SELECT max(o.generation) FROM execution_operations o
    WHERE o.workspace_id=e.workspace_id AND o.environment_id=e.id AND o.kind<>'network'),0))
WHERE e.workspace_id = @workspace_id;

-- name: RaiseServerUpdateGenerations :exec
UPDATE servers s SET update_generation=GREATEST(s.update_generation,COALESCE((
    SELECT max(o.generation) FROM execution_operations o
    WHERE o.workspace_id=s.workspace_id AND o.server_id=s.id AND o.kind IN ('network','update')),0)),last_seen=NULL
WHERE s.workspace_id = @workspace_id;

-- name: SetOperationJob :exec
UPDATE execution_operations SET job_id = @job_id::bigint WHERE workspace_id = @workspace_id AND id = @id;

-- name: DeleteEnvironmentCrossHostRuntime :exec
DELETE FROM cross_host_runtime WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: TakeRecoveryProcessSharedLock :one
-- Installation-wide fence: serving sessions hold this shared while recovery is offline.
SELECT pg_try_advisory_lock_shared(@key::bigint) AS acquired;

-- name: TakeRecoveryProcessLock :one
-- Installation-wide fence: granted only once no serving session holds the shared lock.
SELECT pg_try_advisory_xact_lock(@key::bigint) AS acquired;

-- name: ServerControllerInventory :many
SELECT r.unit_id,p.plan FROM visual_runtime r
JOIN visual_plans p ON p.id=r.plan_id AND p.workspace_id=r.workspace_id
WHERE r.workspace_id = @workspace_id AND r.server_id = @server_id
    AND NOT EXISTS(SELECT 1 FROM cross_host_runtime c WHERE c.workspace_id=r.workspace_id AND c.unit_id=r.unit_id)
UNION ALL
SELECT unit_id,inventory->'plan' FROM cross_host_runtime WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: WorkspaceHasReservations :one
SELECT EXISTS(SELECT 1 FROM cross_host_reservations WHERE workspace_id = @workspace_id);

-- name: WorkspaceRecoveryHeader :one
SELECT name,require_mfa,clock_timestamp()::timestamptz AS captured_at FROM workspaces WHERE id = @workspace_id;

-- name: RecoveryRowAttribution :one
SELECT attributed_hash FROM cross_host_recovery_rows
WHERE workspace_id = @workspace_id AND table_name = @table_name AND source_hash = @source_hash;

-- name: UpsertRecoveryRowAttribution :exec
INSERT INTO cross_host_recovery_rows(workspace_id,table_name,source_hash,attributed_hash)
VALUES(@workspace_id,@table_name,@source_hash,@attributed_hash)
ON CONFLICT(workspace_id,table_name,source_hash) DO UPDATE SET attributed_hash=excluded.attributed_hash;

-- name: SetRecoveredEnvironmentRuntime :exec
UPDATE environments SET applied_revision_id = @applied_revision_id,runtime_operation_id = @runtime_operation_id
WHERE workspace_id = @workspace_id AND id = @id;

-- name: SetRecoveredOperationVisualPlan :exec
UPDATE execution_operations SET visual_plan_id = @visual_plan_id WHERE workspace_id = @workspace_id AND id = @id;
