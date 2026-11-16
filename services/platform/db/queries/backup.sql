-- name: AllBackupDestinationAccess :many
SELECT workspace_id,id,encrypted_access FROM backup_destinations FOR UPDATE;
-- name: RewrapBackupDestinationAccess :exec
UPDATE backup_destinations SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllBackupPolicyAccess :many
SELECT workspace_id,id,encrypted_access FROM backup_policies FOR UPDATE;
-- name: RewrapBackupPolicyAccess :exec
UPDATE backup_policies SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;

-- name: ListBackupDestinations :many
SELECT configuration FROM backup_destinations WHERE workspace_id = @workspace_id ORDER BY id LIMIT 100;

-- name: CountBackupDestinations :one
SELECT count(*) FROM backup_destinations WHERE workspace_id = @workspace_id;

-- name: InsertBackupDestination :exec
INSERT INTO backup_destinations(id,workspace_id,configuration,encrypted_access)
VALUES(@id,@workspace_id,@configuration,@encrypted_access);

-- name: BackupDestinationExists :one
SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE workspace_id = @workspace_id AND id = @id);

-- name: BackupDestinationConfiguration :one
SELECT configuration FROM backup_destinations WHERE workspace_id = @workspace_id AND id = @id;

-- name: BackupDestinationAccess :one
SELECT configuration,encrypted_access FROM backup_destinations WHERE workspace_id = @workspace_id AND id = @id;

-- name: ListBackupPolicies :many
SELECT configuration FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id ORDER BY id LIMIT 100;

-- name: CountBackupPolicies :one
SELECT count(*) FROM backup_policies WHERE workspace_id = @workspace_id;

-- name: BackupPolicyConfiguration :one
SELECT configuration FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: LockBackupPolicyConfiguration :one
SELECT configuration FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id FOR UPDATE;

-- name: UpsertBackupPolicy :execrows
-- The conflict target is the global primary key, so the update must not take
-- over a policy that belongs to another workspace or environment.
INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access)
VALUES(@id,@workspace_id,@environment_id,@server_id,@configuration,@encrypted_access)
ON CONFLICT(id) DO UPDATE SET configuration=excluded.configuration,encrypted_access=excluded.encrypted_access
WHERE backup_policies.workspace_id=excluded.workspace_id AND backup_policies.environment_id=excluded.environment_id;

-- name: BackupPolicyStatus :one
SELECT status FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: BackupPolicyAccess :one
SELECT encrypted_access,configuration FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND server_id = @server_id AND id = @id;

-- name: LockBackupPolicyReport :one
SELECT configuration,status_sequence,status FROM backup_policies
WHERE workspace_id = @workspace_id AND id = @id AND server_id = @server_id FOR UPDATE;

-- name: SaveBackupPolicyReport :exec
UPDATE backup_policies SET status = @status,status_sequence = @status_sequence
WHERE workspace_id = @workspace_id AND id = @id;

-- name: DisableEnvironmentBackupPolicies :exec
UPDATE backup_policies SET configuration=jsonb_set(configuration,'{enabled}','false')
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: ListServerBackupStatuses :many
SELECT id,status FROM backup_policies WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: ClaimDueCoordinatedBackupPolicy :one
-- unscoped: the coordinated scheduler sweeps every workspace and moves each policy to the
-- back of the scan before handling it within its own workspace, so a policy that fails
-- cannot hold the head of the scan. pass_started gives each policy one turn per pass.
-- A workspace this pass has stalled on keeps its place, so its other policies go first next time.
UPDATE backup_policies SET coordinated_checked_at=clock_timestamp()
WHERE id=(SELECT p.id FROM backup_policies p
    WHERE p.configuration->>'coordinated'='true' AND p.configuration->>'enabled'='true'
        AND p.status->>'enabled'='true' AND p.status->>'generation'=p.configuration->>'generation'
        AND p.status->>'next'>'0001-01-01T00:00:00Z' AND p.status->>'next'<= @due_before::text
        AND p.coordinated_checked_at < @pass_started::timestamptz
        AND p.workspace_id <> ALL(@stalled_workspaces::uuid[])
    ORDER BY p.coordinated_checked_at,p.id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING workspace_id,server_id,id;

-- name: LockCoordinatedBackupPolicy :one
SELECT configuration,status FROM backup_policies
WHERE workspace_id = @workspace_id AND server_id = @server_id AND id = @id FOR UPDATE;

-- name: SaveBackupPolicyStatus :exec
UPDATE backup_policies SET status = @status WHERE workspace_id = @workspace_id AND id = @id;

-- name: BackupPolicyMaintenanceReserved :one
-- A coordinated maintenance operation holds its policy until it finishes.
SELECT EXISTS(SELECT 1 FROM cross_host_operations h
    JOIN execution_operations o ON o.workspace_id=h.workspace_id AND o.id=h.id
    WHERE h.workspace_id = @workspace_id AND h.environment_id = @environment_id
        AND o.state IN ('queued','dispatched','running','needs_attention')
        AND (o.command->'crosshost'->'maintenance'->'effect'->'backup'->'installation'->'policy'->>'id' = @policy_id::text
            OR o.command->'crosshost'->'maintenance'->'effect'->'upgrade'->'plan'->>'policyId' = @policy_id::text));

-- name: InsertBackupCutover :exec
INSERT INTO backup_cutovers(workspace_id,environment_id,revision_id,policy_id,source_plan_id,configuration)
VALUES(@workspace_id,@environment_id,@revision_id,@policy_id,@source_plan_id,@configuration);

-- name: ClaimRestoreTarget :one
-- A verified restore's target becomes the environment's data volume when a cutover is prepared,
-- before any plan mounts it, with the PostgreSQL major the restore initialised it with, as an
-- upgrade's target does when it is planned. Another environment's or a deleted volume returns no row.
INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_major)
VALUES(@workspace_id,@environment_id,@unit_id,@id,sqlc.narg(postgres_major))
ON CONFLICT(workspace_id,id) DO UPDATE SET postgres_major = COALESCE(managed_volumes.postgres_major,EXCLUDED.postgres_major)
WHERE managed_volumes.environment_id = EXCLUDED.environment_id AND NOT managed_volumes.deleted
RETURNING postgres_major;

-- name: OperationCommandByRequestKey :one
SELECT command FROM execution_operations WHERE workspace_id = @workspace_id AND request_key = @request_key;

-- name: RecordBackupSnapshotReceipts :exec
-- A listed snapshot keeps the time management first received it, one an upload lists gets
-- management's clock now, and one no longer listed loses its receipt.
UPDATE backup_policies p SET snapshot_receipts = COALESCE((
    SELECT jsonb_object_agg(s.snapshot, COALESCE(p.snapshot_receipts->s.snapshot, to_jsonb(clock_timestamp())))
    FROM unnest(@listed::text[]) AS s(snapshot)
    WHERE p.snapshot_receipts->s.snapshot IS NOT NULL OR s.snapshot = ANY(@uploaded::text[])
), '{}')
WHERE workspace_id = @workspace_id AND id = @policy_id;

-- name: BackupSnapshotReceipts :one
SELECT snapshot_receipts FROM backup_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: InsertBackupRecovery :exec
INSERT INTO backup_recoveries(id,workspace_id,environment_id,policy_id,operation_id,source_server_id,server_id,plan_id,snapshot,first_reported_at,target_volume_id,created_at)
VALUES(@id,@workspace_id,@environment_id,@policy_id,@operation_id,@source_server_id,@server_id,@plan_id,@snapshot,@first_reported_at,@target_volume_id,@created_at);

-- name: BackupRecoveryAccess :one
-- A recovery's repository access is released only to the server it was issued to, for the
-- operation it was issued as, while the policy is still the one it froze.
SELECT p.encrypted_access,p.configuration FROM backup_recoveries r
JOIN backup_policies p ON p.workspace_id = r.workspace_id AND p.environment_id = r.environment_id AND p.id = r.policy_id
WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id AND r.id = @id
AND r.operation_id = @operation_id AND r.server_id = @server_id AND r.policy_id = @policy_id;

-- name: RecordBackupRecoveryResult :exec
-- Reports stop once the operation is terminal, so the succeeded report is the only writer.
UPDATE backup_recoveries SET result = @result WHERE workspace_id = @workspace_id AND operation_id = @operation_id;

-- name: LockRecoveryDelivery :one
-- A restore of the polling server, locked to decide its delivery. Held is whether an agent may
-- have it: its values were released to one, or it reported.
SELECT o.command,o.state,o.cancel_requested,(o.report_sequence > 0 OR EXISTS(
    SELECT 1 FROM secret_releases r WHERE r.workspace_id = o.workspace_id AND r.operation_id = o.id))::boolean AS held
FROM execution_operations o
WHERE o.workspace_id = @workspace_id AND o.id = @id AND o.server_id = @server_id AND o.kind = 'restore'
FOR UPDATE OF o;

-- name: FinishUndeliveredRecovery :exec
UPDATE execution_operations SET state = @state, note = @note::text || CASE WHEN note <> '' THEN ' ' || note ELSE '' END
WHERE workspace_id = @workspace_id AND id = @id;

-- name: ListBackupRecoveries :many
SELECT r.id,r.operation_id,r.source_server_id,r.server_id,r.plan_id,r.snapshot,r.first_reported_at,r.target_volume_id,r.created_at,r.result,o.state,o.note,coalesce(v.deleted,false)::boolean AS target_deleted,
 r.discard_operation_id,d.state AS discard_state,d.note AS discard_note
FROM backup_recoveries r JOIN execution_operations o ON o.workspace_id = r.workspace_id AND o.id = r.operation_id
LEFT JOIN execution_operations d ON d.workspace_id = r.workspace_id AND d.id = r.discard_operation_id
LEFT JOIN managed_volumes v ON v.workspace_id = r.workspace_id AND v.environment_id = r.environment_id AND v.id = r.target_volume_id
WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id AND r.policy_id = @policy_id
ORDER BY r.created_at DESC, r.id LIMIT 100;

-- name: BackupRecovery :one
-- One restore of a policy's snapshot onto another server, with its operation's state and its latest
-- discard's. A cutover and a discard of its volume are decided one at a time under the workspace
-- write lock; locking this row too would deadlock a discard against the restore's own report, which
-- locks the server first. A restore's result is written once, when its operation ends.
SELECT r.server_id,r.plan_id,r.snapshot,r.target_volume_id,r.result,o.state,d.state AS discard_state
FROM backup_recoveries r JOIN execution_operations o ON o.workspace_id = r.workspace_id AND o.id = r.operation_id
LEFT JOIN execution_operations d ON d.workspace_id = r.workspace_id AND d.id = r.discard_operation_id
WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id AND r.policy_id = @policy_id AND r.id = @id;

-- name: SetBackupRecoveryDiscard :exec
UPDATE backup_recoveries SET discard_operation_id = @discard_operation_id
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: ExposedBackupDestinations :many
-- A destination is exposed while a revoked server holds its storage keys: the server a policy using
-- it is bound to, or one a restore of such a policy was recorded for, whether or not the restore
-- was released to it. Only an S3 destination has keys, and a container job is never given them.
SELECT DISTINCT d.id
FROM backup_destinations d
JOIN backup_policies p ON p.workspace_id = d.workspace_id AND p.configuration->>'destinationId' = d.id::text
    AND p.configuration->>'method' <> 'container-job'
JOIN servers s ON s.workspace_id = p.workspace_id AND s.revoked AND (s.id = p.server_id OR EXISTS(
    SELECT 1 FROM backup_recoveries r WHERE r.workspace_id = p.workspace_id AND r.policy_id = p.id AND r.server_id = s.id))
WHERE d.workspace_id = @workspace_id AND d.configuration->>'kind' = 's3';

-- name: BackupDestinationPolicies :many
-- The policies using a workspace's destinations, with their environments. A container job uses none.
SELECT (p.configuration->>'destinationId')::uuid AS destination_id, p.id, (p.configuration->>'name')::text AS name,
    e.project_id, e.id AS environment_id, e.name AS environment_name
FROM backup_policies p JOIN environments e ON e.workspace_id = p.workspace_id AND e.id = p.environment_id
WHERE p.workspace_id = @workspace_id AND p.configuration->>'method' <> 'container-job'
ORDER BY e.name, p.configuration->>'name', p.id;
