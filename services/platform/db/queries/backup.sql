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
        AND o.state NOT IN ('succeeded','failed','cancelled')
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
