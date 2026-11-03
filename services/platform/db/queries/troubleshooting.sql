-- name: GetTroubleshootingPolicy :one
SELECT enabled,owners_only,generation FROM troubleshooting_policies
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: SaveTroubleshootingPolicy :exec
INSERT INTO troubleshooting_policies(workspace_id,environment_id,enabled,owners_only,generation,updated_by)
VALUES(@workspace_id,@environment_id,@enabled,@owners_only,@generation,@updated_by)
ON CONFLICT(workspace_id,environment_id) DO UPDATE SET
 enabled = excluded.enabled,owners_only = excluded.owners_only,generation = excluded.generation,
 updated_by = excluded.updated_by,updated_at = now();

-- name: DisableWorkspaceTroubleshootingPolicies :exec
UPDATE troubleshooting_policies SET enabled = false,generation = generation + 1,updated_at = now()
WHERE workspace_id = @workspace_id AND enabled;

-- name: TroubleshootingEnvironment :one
SELECT e.applied_revision_id,d.server_id AS primary_server_id,r.specification
FROM environments e
JOIN deployment_units d ON d.workspace_id = e.workspace_id AND d.environment_id = e.id AND d.primary_unit
LEFT JOIN specification_revisions r ON r.workspace_id = e.workspace_id AND r.environment_id = e.id AND r.id = e.applied_revision_id
WHERE e.workspace_id = @workspace_id AND e.project_id = @project_id AND e.id = @environment_id;

-- name: TroubleshootingEnvironmentExists :one
SELECT EXISTS(SELECT 1 FROM environments
WHERE workspace_id = @workspace_id AND project_id = @project_id AND id = @environment_id);

-- name: TroubleshootingUnit :one
SELECT d.id AS unit_id,s.capabilities,s.controller_generation
FROM deployment_units d
JOIN servers s ON s.workspace_id = d.workspace_id AND s.id = d.server_id
WHERE d.workspace_id = @workspace_id AND d.environment_id = @environment_id AND d.server_id = @server_id::uuid
 AND NOT s.revoked AND NOT s.awaiting_transfer AND s.credential_hash IS NOT NULL;

-- name: LockTroubleshootingAdmission :exec
SELECT pg_advisory_xact_lock(4281642303);

-- name: TroubleshootingAdmissionCounts :one
-- unscoped: the installation and per-actor limits span workspaces; each result is only a count.
SELECT
 count(*) AS installation,
 count(*) FILTER (WHERE workspace_id = @workspace_id) AS workspace,
 count(*) FILTER (WHERE actor_id = @actor_id) AS actor,
 count(*) FILTER (WHERE workspace_id = @workspace_id AND server_id = @server_id) AS server,
 count(*) FILTER (WHERE workspace_id = @workspace_id AND server_id = @server_id AND environment_id = @environment_id AND application_id = @application_id AND instance = @instance) AS target
FROM troubleshooting_sessions
WHERE state <> 'closed' AND deadline_at > now() AND (state <> 'offered' OR ticket_expires_at > now());

-- name: InsertTroubleshootingSession :one
INSERT INTO troubleshooting_sessions(id,workspace_id,environment_id,unit_id,server_id,application_id,instance,actor_id,auth_version,browser_session_hash,controller_generation,state,ticket_hash,ticket_expires_at,deadline_at)
VALUES(@id,@workspace_id,@environment_id,@unit_id,@server_id,@application_id,@instance,@actor_id,@auth_version,@browser_session_hash,@controller_generation,'offered',@ticket_hash,@ticket_expires_at,@deadline_at)
RETURNING id,environment_id,server_id,application_id,instance,actor_id,state,created_at,deadline_at,closed_at,close_reason;

-- name: ExpireTroubleshootingSessions :many
UPDATE troubleshooting_sessions SET state = 'closed',closed_at = now(),close_reason = 'expired',ticket_hash = NULL,browser_session_hash = NULL
WHERE workspace_id = @workspace_id AND state <> 'closed' AND (deadline_at <= now() OR (state = 'offered' AND ticket_expires_at <= now()))
RETURNING id,actor_id;

-- name: GetTroubleshootingSessionActor :one
SELECT actor_id FROM troubleshooting_sessions
WHERE workspace_id = @workspace_id AND id = @id AND state <> 'closed'
FOR UPDATE;

-- name: CloseTroubleshootingSession :exec
UPDATE troubleshooting_sessions SET state = 'closed',closed_at = now(),close_reason = @close_reason::text,ticket_hash = NULL,browser_session_hash = NULL
WHERE workspace_id = @workspace_id AND id = @id AND state <> 'closed';

-- name: CloseWorkspaceTroubleshootingSessions :many
UPDATE troubleshooting_sessions SET state = 'closed',closed_at = now(),close_reason = @close_reason::text,ticket_hash = NULL,browser_session_hash = NULL
WHERE workspace_id = @workspace_id AND state <> 'closed'
RETURNING id,actor_id;

-- name: CloseEnvironmentTroubleshootingSessions :many
UPDATE troubleshooting_sessions t SET state = 'closed',closed_at = now(),close_reason = @close_reason::text,ticket_hash = NULL,browser_session_hash = NULL
WHERE t.workspace_id = @workspace_id AND t.environment_id = @environment_id AND t.state <> 'closed'
 AND (NOT @only_non_owners::boolean OR NOT EXISTS(SELECT 1 FROM memberships m WHERE m.workspace_id = t.workspace_id AND m.account_id = t.actor_id AND m.role = 'owner'))
RETURNING t.id;

-- name: ListOpenTroubleshootingSessions :many
SELECT id,environment_id,server_id,application_id,instance,actor_id,state,created_at,deadline_at,closed_at,close_reason
FROM troubleshooting_sessions
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND state <> 'closed'
 AND deadline_at > now() AND (state <> 'offered' OR ticket_expires_at > now())
ORDER BY created_at DESC,id LIMIT 8;

-- name: ListTroubleshootingSessions :many
SELECT id,environment_id,server_id,application_id,instance,actor_id,state,created_at,deadline_at,closed_at,close_reason
FROM troubleshooting_sessions
WHERE workspace_id = @workspace_id
ORDER BY created_at DESC,id LIMIT 100;

-- name: RecentlyAudited :one
SELECT EXISTS(SELECT 1 FROM audit_events
WHERE workspace_id = @workspace_id AND actor_id = @actor_id AND action = @action AND created_at > now() - interval '1 hour');
