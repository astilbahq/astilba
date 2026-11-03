-- name: ListServers :many
SELECT id,name,role,last_seen,capabilities,preflight,revoked,rotation_requested,awaiting_transfer,preview_only
FROM servers WHERE workspace_id = @workspace_id ORDER BY name,id LIMIT 100;

-- name: CountServers :one
SELECT count(*) FROM servers WHERE workspace_id = @workspace_id;

-- name: InsertServer :one
INSERT INTO servers(id,workspace_id,name,role,enrollment_hash,enrollment_expires,preview_only)
VALUES(@id,@workspace_id,@name,@role,@enrollment_hash,@enrollment_expires,@preview_only)
RETURNING id,name,role,last_seen,capabilities,preflight,revoked,rotation_requested,awaiting_transfer,preview_only;

-- name: RenewServerEnrollment :one
UPDATE servers SET enrollment_hash = @enrollment_hash,enrollment_expires = @enrollment_expires,
    credential_hash=NULL,previous_hash=NULL,previous_until=NULL,revoked=false,rotation_requested=false
WHERE workspace_id = @workspace_id AND id = @id
RETURNING id,name,role,last_seen,capabilities,preflight,revoked,rotation_requested,awaiting_transfer,preview_only;

-- name: RequestServerRotation :execrows
UPDATE servers SET rotation_requested=true
WHERE workspace_id = @workspace_id AND id = @id AND NOT revoked AND credential_hash IS NOT NULL;

-- name: RevokeServer :execrows
UPDATE servers SET revoked=true,credential_hash=NULL,previous_hash=NULL,enrollment_hash=NULL,rotation_requested=false
WHERE workspace_id = @workspace_id AND id = @id;

-- name: AdvanceServerUpdateGeneration :one
UPDATE servers SET update_generation=update_generation+1
WHERE workspace_id = @workspace_id AND id = @id AND NOT revoked AND credential_hash IS NOT NULL
RETURNING capabilities,update_generation;

-- name: PrepareServerTransfer :one
UPDATE servers SET enrollment_hash = @enrollment_hash,enrollment_expires = @enrollment_expires,
    credential_hash=NULL,previous_hash=NULL,previous_until=NULL,poll_id=NULL,poll_until=NULL,revoked=false
WHERE workspace_id = @workspace_id AND id = @id AND awaiting_transfer
RETURNING controller_generation;

-- name: AuthenticateAgent :one
-- unscoped: the presented agent credential establishes the workspace.
SELECT workspace_id,role,(credential_hash <> @credential_hash)::boolean AS previous,
    COALESCE((capabilities->>'credentialFreeVisualCancel')::boolean,false)::boolean AS credential_free_visual_cancel
FROM servers
WHERE id = @server_id AND NOT revoked AND NOT awaiting_transfer
    AND (credential_hash = @credential_hash OR (previous_hash = @credential_hash AND previous_until>now()));

-- name: RecordAgentHeartbeat :one
UPDATE servers SET last_seen=now(),capabilities = @capabilities,preflight = @preflight
WHERE workspace_id = @workspace_id AND id = @server_id AND NOT revoked AND NOT awaiting_transfer
    AND (credential_hash = @credential_hash OR (previous_hash = @credential_hash AND previous_until>now()))
RETURNING role,rotation_requested,preview_only;

-- name: RotateAgentCredential :execrows
-- The same candidate is an idempotent retry. A previous credential cannot
-- initiate a new exchange.
UPDATE servers SET
    previous_hash=CASE WHEN credential_hash = @candidate_hash THEN previous_hash ELSE credential_hash END,
    previous_until=CASE WHEN credential_hash = @candidate_hash THEN previous_until ELSE now()+interval '2 minutes' END,
    credential_hash = @candidate_hash,rotation_requested=false
WHERE workspace_id = @workspace_id AND id = @server_id AND NOT revoked AND NOT awaiting_transfer
    AND ((credential_hash = @credential_hash AND rotation_requested)
        OR (credential_hash = @candidate_hash
            AND (credential_hash = @credential_hash OR (previous_hash = @credential_hash AND previous_until>now()))));

-- name: LockAgentCredential :one
-- Locks an authenticated agent's server row before it acts on an operation, so
-- revocation, rotation and controller transfer cannot race acceptance.
-- A cleared credential compares as NULL and is reported as invalid.
SELECT COALESCE(NOT revoked AND NOT awaiting_transfer
    AND (credential_hash = @credential_hash OR (previous_hash = @credential_hash AND previous_until>now())),
    false)::boolean AS valid
FROM servers WHERE workspace_id = @workspace_id AND id = @server_id FOR UPDATE;

-- name: LockAgentEnrollment :one
-- unscoped: the enrollment token or existing credential establishes the workspace.
-- Accepts an unexpired enrollment token or the credential already issued for
-- this server, so an interrupted exchange can be retried.
SELECT workspace_id,controller_generation,awaiting_transfer,preview_only,(machine_id IS NULL)::boolean AS fresh
FROM servers
WHERE id = @server_id AND NOT revoked AND (machine_id IS NULL OR machine_id = @machine_id)
    AND ((enrollment_hash = @enrollment_hash AND enrollment_expires>now() AND credential_hash IS NULL)
        OR credential_hash = @credential_hash)
FOR UPDATE;

-- name: StagedRecoveryInventory :one
SELECT r.inventory FROM cross_host_recovery_servers r
JOIN cross_host_recoveries h ON h.workspace_id=r.workspace_id AND h.parent_id=r.parent_id
WHERE r.workspace_id = @workspace_id AND r.server_id = @server_id AND r.machine_id = @machine_id
    AND r.controller_generation = @controller_generation AND NOT h.opened;

-- name: VerifyStagedRecoveryServer :exec
UPDATE cross_host_recovery_servers SET verified=true
WHERE workspace_id = @workspace_id AND server_id = @server_id AND machine_id = @machine_id
    AND controller_generation = @controller_generation;

-- name: CompleteAgentEnrollment :one
UPDATE servers SET credential_hash = @credential_hash,enrollment_hash=NULL,enrollment_expires=NULL,
    machine_id = @machine_id,capabilities = @capabilities,preflight = @preflight,last_seen=now(),awaiting_transfer=false
WHERE workspace_id = @workspace_id AND id = @server_id
RETURNING role,rotation_requested,preview_only;

-- name: ServerCapabilities :one
SELECT capabilities FROM servers WHERE workspace_id = @workspace_id AND id = @id;

-- name: ServerHasActiveOperations :one
SELECT EXISTS(SELECT 1 FROM execution_operations
    WHERE workspace_id = @workspace_id AND server_id = @server_id AND state NOT IN ('succeeded','failed','cancelled'));
