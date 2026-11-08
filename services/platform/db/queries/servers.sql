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
-- Revoking a preview host released its previews without anything confirming their removal, so it
-- comes back only as a clean host: without a machine binding, enrollment checks it has no
-- containers. Every other renewal keeps the same machine.
UPDATE servers SET enrollment_hash = @enrollment_hash,enrollment_expires = @enrollment_expires,
    credential_hash=NULL,previous_hash=NULL,previous_until=NULL,revoked=false,rotation_requested=false,
    machine_id=CASE WHEN revoked AND preview_only THEN NULL ELSE machine_id END
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

-- name: SettleRevokedServerOperations :exec
-- A revoked server will never report again. Roots are left to the coordinator, which still has the
-- remaining hosts to release, and a standalone network removal stays visible until peers converge.
-- The sequence answers any later report from a renewed host without letting it change the outcome,
-- so that host can acknowledge its journal and take work again.
UPDATE execution_operations SET state='cancelled',cancel_requested=true,report_sequence = @report_sequence,note='The server was revoked before reporting, so what it did is unknown.'
WHERE workspace_id = @workspace_id AND server_id = @server_id AND state NOT IN ('succeeded','failed','cancelled')
    AND (parent_id IS NOT NULL OR kind NOT IN ('crosshost','network'));

-- name: RejectRevokedServerUpgrades :exec
UPDATE supported_upgrades SET status=jsonb_set(jsonb_set(status,'{phase}','"rejected"'),'{note}','"The server was revoked, so the outcome of this upgrade is unknown."')
WHERE workspace_id = @workspace_id AND server_id = @server_id AND status->>'phase' NOT IN ('planned','rejected','complete','returned');

-- name: ParkedMaintenanceOnPrimary :many
-- A parent waiting for the owner's choice has no running job. With its primary gone there is no
-- choice left to make.
SELECT o.id FROM execution_operations o JOIN cross_host_operations c ON c.id=o.id AND c.workspace_id=o.workspace_id
WHERE o.workspace_id = @workspace_id AND o.server_id = @server_id AND o.parent_id IS NULL AND o.kind='crosshost'
    AND o.state NOT IN ('succeeded','failed','cancelled') AND c.phase='maintenance-owner';

-- name: ResumeMaintenanceSource :exec
UPDATE cross_host_operations SET phase='maintenance-resume-source' WHERE workspace_id = @workspace_id AND id = @id;

-- name: AttachOperationJob :exec
UPDATE execution_operations SET state='running',job_id = @job_id::bigint WHERE workspace_id = @workspace_id AND id = @id;

-- name: RevokedServersAmong :many
SELECT id,name FROM servers WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]) AND revoked;

-- name: DeleteAbandonedCrossHostRuntime :exec
-- A unit that a plan left behind on a revoked server can't report its own removal.
DELETE FROM cross_host_runtime WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND NOT(unit_id = ANY(@units::uuid[]));

-- name: RecordRevokedUnitRuntime :exec
-- Every active unit of an applied plan has a runtime row, which recovery relies on.
INSERT INTO cross_host_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,inventory,cpu_millis,memory_mib)
VALUES(@workspace_id,@environment_id,@unit_id,@server_id,@plan_id,@inventory,@cpu_millis,@memory_mib)
ON CONFLICT(workspace_id,unit_id) DO UPDATE SET plan_id=EXCLUDED.plan_id,inventory=EXCLUDED.inventory,observed_at=now();

-- name: RevokedServerInActiveRoot :one
-- Coordinators decide what to skip from the revocation itself, and a plan that leaves the server
-- behind decided on it too, so it must hold until they finish.
SELECT EXISTS(SELECT 1 FROM execution_operations o JOIN servers s ON s.workspace_id=o.workspace_id
    WHERE o.workspace_id = @workspace_id AND s.id = @server_id AND s.revoked AND o.parent_id IS NULL AND o.kind='crosshost'
        AND o.state NOT IN ('succeeded','failed','cancelled')
        AND (o.command->'crosshost'->'plan'->'distributed'->'units' @> jsonb_build_array(jsonb_build_object('plan',jsonb_build_object('serverId',@server_id::text)))
            OR o.command->'crosshost'->'plan'->'distributed'->'abandoned' @> jsonb_build_array(@server_id::text)));

-- name: CancelUnstartedReleaseCommands :exec
-- Every root naming the server now ends before publication, and a release command runs once and
-- can't be taken back. This shares the revocation's transaction so none can start in between.
UPDATE execution_operations child SET cancel_requested=true FROM execution_operations root
WHERE child.workspace_id = @workspace_id AND root.workspace_id = @workspace_id AND child.parent_id=root.id
    AND child.state NOT IN ('succeeded','failed','cancelled') AND child.command->'crosshost'->>'phase'='hook'
    AND root.command->'crosshost'->'plan'->'distributed'->'units' @> jsonb_build_array(jsonb_build_object('plan',jsonb_build_object('serverId',@server_id::text)));

-- name: CancelHeldPreparation :exec
-- This releases a held secondary. A held primary waits for a local retry, which this also ends.
UPDATE execution_operations SET cancel_requested=true
WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND state='needs_attention' AND command->'crosshost'->>'phase'='prepare';

-- name: EnvironmentVolumes :many
SELECT id FROM managed_volumes WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND unit_id = @unit_id AND NOT deleted ORDER BY id;

-- name: SecondaryUnitOnServer :one
SELECT id FROM deployment_units WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND server_id = @server_id AND NOT primary_unit;

-- name: DemotePrimaryUnit :exec
-- The former primary is detached from its revoked server, so nothing reuses its identity there and
-- picks up the data its volumes still hold on that host.
UPDATE deployment_units SET primary_unit=false,server_id=NULL WHERE workspace_id = @workspace_id AND id = @id AND primary_unit;

-- name: InsertSecondaryUnit :exec
INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES(@workspace_id,@id,@environment_id,@server_id,false);

-- name: PromoteUnit :exec
UPDATE deployment_units SET primary_unit=true WHERE workspace_id = @workspace_id AND id = @id AND NOT primary_unit;

-- name: MoveEnvironmentVolumes :exec
UPDATE managed_volumes SET unit_id = @unit_id WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: MoveEnvironmentResources :exec
UPDATE managed_resources SET unit_id = @unit_id WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: DropUnusedAbandonedVolumes :exec
-- A volume the plan doesn't declare isn't created on the new primary, and the revoked server holds the only copy.
UPDATE managed_volumes SET deleted=true WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = ANY(@abandoned::uuid[]) AND NOT(id = ANY(@declared::uuid[]));

-- name: ReleaseRevokedRouteClaims :exec
DELETE FROM route_claims c WHERE c.workspace_id = @workspace_id AND c.environment_id = @environment_id
    AND c.server_id IN (SELECT s.id FROM servers s WHERE s.workspace_id = @workspace_id AND s.revoked);

-- name: ServerName :one
SELECT name FROM servers WHERE workspace_id = @workspace_id AND id = @id;
