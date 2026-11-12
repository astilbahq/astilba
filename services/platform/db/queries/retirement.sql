-- name: EnvironmentState :one
SELECT state FROM environments WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockRetirementEnvironment :one
-- The primary unit's server is where a deployed environment's resources are.
SELECT e.name,e.state,e.draft_revision_id,e.applied_revision_id,e.retirement_operation_id,
 d.id AS unit_id,d.server_id
FROM environments e
JOIN deployment_units d ON d.workspace_id = e.workspace_id AND d.environment_id = e.id AND d.primary_unit
WHERE e.workspace_id = @workspace_id AND e.project_id = @project_id AND e.id = @id
FOR UPDATE OF e;

-- name: RetirementHolds :one
-- What stops an environment being retired now. server_busy mirrors execution_active_server, which
-- the retirement command would otherwise meet as a bare conflict. cross_host is what management
-- expects the environment to run across servers now: a unit a cross-host deployment recorded, as
-- ServerControllerInventory lists it, or a private endpoint its applied placement still publishes.
-- Consolidating onto the primary is still a cross-host deployment, which keeps the primary's
-- runtime row; only a later deployment without a placement removes both. A replaced primary is
-- detached from its revoked server, so it leaves no lasting hold; the secondary unit rows
-- themselves are never deleted.
SELECT
 EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.environment_id = sqlc.arg(environment_id)::uuid
  AND o.parent_id IS NULL AND o.state NOT IN ('succeeded','failed','cancelled'))::boolean AS active_operation,
 EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.server_id = sqlc.narg(server_id)::uuid
  AND o.kind <> 'crosshost' AND NOT (o.kind = 'network' AND COALESCE(o.command->'network'->>'action','') = 'restrict')
  AND o.state NOT IN ('succeeded','failed','cancelled') AND (o.kind <> 'pipeline' OR o.state <> 'queued'))::boolean AS server_busy,
 EXISTS(SELECT 1 FROM supported_upgrades u WHERE u.workspace_id = @workspace_id AND u.environment_id = @environment_id
  AND (u.status->>'phase' NOT IN ('planned','rejected','complete','returned') OR (u.status->>'keepUntil')::timestamptz > now()))::boolean AS upgrade_hold,
 EXISTS(SELECT 1 FROM backup_cutovers c JOIN environments e ON e.workspace_id = c.workspace_id AND e.id = c.environment_id
  WHERE c.workspace_id = @workspace_id AND c.environment_id = @environment_id AND c.revision_id = e.draft_revision_id
  AND e.applied_revision_id IS DISTINCT FROM c.revision_id)::boolean AS cutover_hold,
 (EXISTS(SELECT 1 FROM cross_host_runtime r WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id)
  OR EXISTS(SELECT 1 FROM network_endpoints n WHERE n.workspace_id = @workspace_id AND n.environment_id = @environment_id AND NOT n.retired))::boolean AS cross_host;

-- name: RetirementServer :one
SELECT revoked,awaiting_transfer,capabilities FROM servers WHERE workspace_id = @workspace_id AND id = @id;

-- name: RetirementOperationState :one
SELECT state FROM execution_operations WHERE workspace_id = @workspace_id AND environment_id = sqlc.arg(environment_id)::uuid AND id = @id;

-- name: SetEnvironmentRetirement :exec
UPDATE environments SET state = @state,retired_at = now(),retired_by = sqlc.arg(retired_by)::uuid,
 retirement_operation_id = sqlc.narg(retirement_operation_id),retired_without_host = @retired_without_host
WHERE workspace_id = @workspace_id AND id = @id;

-- name: CompleteEnvironmentRetirement :execrows
UPDATE environments SET state = 'retired'
WHERE workspace_id = @workspace_id AND id = @id AND state = 'retiring' AND retirement_operation_id = sqlc.arg(retirement_operation_id)::uuid;

-- name: RestoreRetiredEnvironment :execrows
UPDATE environments SET state = 'active',retired_at = NULL,retired_by = NULL,retirement_operation_id = NULL,retired_without_host = false
WHERE workspace_id = @workspace_id AND id = @id AND state = 'retired';

-- name: RevokeEnvironmentAutomationCredentials :many
UPDATE automation_credentials SET revoked_at = now()
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND revoked_at IS NULL
RETURNING id;

-- name: DisableEnvironmentPreviewPolicy :many
UPDATE preview_policies SET enabled = false
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
RETURNING id,server_id;

-- name: RequestPolicyPreviewCleanup :many
-- The same request an owner makes for one preview, for every preview the policy still has, whatever
-- its host's state. The cleanup reconcile checks the authority when it runs: a preview whose host is
-- waiting for a verified agent transfer, or whose owner's authority changed, shows as blocked until
-- an owner renews its cleanup, which stays possible on a retired template. Revocation already
-- released any preview it allocated on a revoked host.
UPDATE preview_requests r SET cleanup_requested = true,cleanup_at = now(),next_check_at = now(),
 cleanup_actor = sqlc.arg(actor_id)::uuid,cleanup_auth_version = sqlc.arg(auth_version)::bigint,cleanup_controller = s.controller_generation
FROM servers s
WHERE r.workspace_id = @workspace_id AND r.policy_id = @policy_id AND r.state <> 'deleted'
 AND s.workspace_id = @workspace_id AND s.id = @server_id
RETURNING r.id;

-- name: DisableEnvironmentTroubleshootingPolicy :exec
UPDATE troubleshooting_policies SET enabled = false,generation = generation + 1,updated_by = @updated_by,updated_at = now()
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND enabled;

-- name: ReleaseEnvironmentRouteClaims :exec
DELETE FROM route_claims WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: DeleteEnvironmentRuntime :exec
DELETE FROM visual_runtime WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: ClearEnvironmentApplied :exec
UPDATE environments SET applied_revision_id = NULL WHERE workspace_id = @workspace_id AND id = @id;

-- name: RetiredEnvironmentPlan :one
-- The plan a completed retirement removed, which stays the environment's last deployed configuration.
SELECT (o.command->'visual'->'plan'->>'id')::uuid AS plan_id
FROM environments e
JOIN execution_operations o ON o.workspace_id = e.workspace_id AND o.environment_id = e.id AND o.id = e.retirement_operation_id
WHERE e.workspace_id = @workspace_id AND e.id = @id AND e.state = 'retired' AND o.runtime_outcome = 'removed';

-- name: RouteClaimOwner :one
SELECT environment_id FROM route_claims WHERE workspace_id = @workspace_id AND server_id = @server_id AND claim = @claim;
