-- name: EnvironmentState :one
SELECT state FROM environments WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockRetirementEnvironment :one
-- The primary unit's server is where a deployed environment's resources are.
SELECT e.name,e.state,e.draft_revision_id,e.applied_revision_id,e.retirement_operation_id,e.purge_operation_id,
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
  AND o.parent_id IS NULL AND o.state IN ('queued','dispatched','running','needs_attention'))::boolean AS active_operation,
 EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.server_id = sqlc.narg(server_id)::uuid
  AND o.kind <> 'crosshost' AND NOT (o.kind = 'network' AND COALESCE(o.command->'network'->>'action','') = 'restrict')
  AND o.state IN ('queued','dispatched','running','needs_attention') AND (o.kind <> 'pipeline' OR o.state <> 'queued'))::boolean AS server_busy,
 EXISTS(SELECT 1 FROM supported_upgrades u WHERE u.workspace_id = @workspace_id AND u.environment_id = @environment_id
  AND (u.status->>'phase' NOT IN ('planned','rejected','complete','returned') OR (u.status->>'keepUntil')::timestamptz > now()))::boolean AS upgrade_hold,
 EXISTS(SELECT 1 FROM backup_cutovers c JOIN environments e ON e.workspace_id = c.workspace_id AND e.id = c.environment_id
  WHERE c.workspace_id = @workspace_id AND c.environment_id = @environment_id AND c.revision_id = e.draft_revision_id
  AND e.applied_revision_id IS DISTINCT FROM c.revision_id)::boolean AS cutover_hold,
 (EXISTS(SELECT 1 FROM cross_host_runtime r WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id)
  OR EXISTS(SELECT 1 FROM network_endpoints n WHERE n.workspace_id = @workspace_id AND n.environment_id = @environment_id AND NOT n.retired))::boolean AS cross_host;

-- name: UndeletedTemplatePreviews :many
-- A preview template's previews each hold copies of its secret values until their cleanup or their
-- host's revocation deletes them, which also marks them deleted. Every change to a preview holds
-- the workspace write lock, as a purge does, so none appears or comes back while a purge checks.
SELECT r.number,r.state,r.note
FROM preview_requests r
JOIN preview_policies p ON p.workspace_id = r.workspace_id AND p.id = r.policy_id
WHERE r.workspace_id = @workspace_id AND p.environment_id = @environment_id AND r.state <> 'deleted'
ORDER BY r.number;

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
WHERE e.workspace_id = @workspace_id AND e.id = @id AND e.state IN ('retired','purging','purged') AND o.runtime_outcome = 'removed';

-- name: RouteClaimOwner :one
SELECT environment_id FROM route_claims WHERE workspace_id = @workspace_id AND server_id = @server_id AND claim = @claim;

-- name: SetEnvironmentPurge :exec
UPDATE environments SET state = @state,purged_at = now(),purged_by = sqlc.arg(purged_by)::uuid,
 purge_operation_id = sqlc.narg(purge_operation_id),purged_without_host = @purged_without_host
WHERE workspace_id = @workspace_id AND id = @id;

-- name: CompleteEnvironmentPurge :execrows
UPDATE environments SET state = 'purged'
WHERE workspace_id = @workspace_id AND id = @id AND state = 'purging' AND purge_operation_id = sqlc.arg(purge_operation_id)::uuid;

-- name: MarkVolumesDeleted :exec
UPDATE managed_volumes SET deleted = true
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: EraseEnvironmentSecretValues :exec
-- Purging keeps each version's row, which specifications name, and deletes its value.
UPDATE secret_versions v SET encrypted_value = NULL,erased_at = now()
FROM secrets s
WHERE s.workspace_id = @workspace_id AND s.environment_id = @environment_id
 AND v.workspace_id = s.workspace_id AND v.secret_id = s.id AND v.erased_at IS NULL;

-- name: LiveSecret :one
SELECT id FROM secrets WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id AND retired_at IS NULL;

-- name: SecretInUse :one
-- Whether something that may still deploy names a version of the secret: the draft, the applied
-- revision, the revision a preview policy creates previews from (paused ones resume with it), the
-- runtime's plan, a plan that can still be applied, a live operation, a cross-host unit's plan, or a
-- supported upgrade that has not ended. A version's id appears in a
-- specification only as a secret reference, so finding it in the stored text covers every plan
-- shape, including ones added later.
WITH versions AS (
 SELECT v.id::text AS id FROM secret_versions v WHERE v.workspace_id = @workspace_id AND v.secret_id = sqlc.arg(secret_id)::uuid
), documents AS (
 SELECT r.specification::text AS body FROM specification_revisions r
 JOIN environments e ON e.workspace_id = r.workspace_id AND e.id = r.environment_id
 WHERE e.workspace_id = @workspace_id AND e.id = sqlc.arg(environment_id)::uuid AND (r.id = e.draft_revision_id OR r.id = e.applied_revision_id)
 UNION ALL
 SELECT r.specification::text FROM specification_revisions r
 JOIN preview_policies p ON p.workspace_id = r.workspace_id AND p.environment_id = r.environment_id AND p.revision_id = r.id
 WHERE p.workspace_id = @workspace_id AND p.environment_id = sqlc.arg(environment_id)::uuid
 UNION ALL
 SELECT p.plan::text FROM visual_plans p WHERE p.workspace_id = @workspace_id AND p.environment_id = sqlc.arg(environment_id)::uuid
  AND (p.plan->>'expiresAt')::timestamptz > now()
 UNION ALL
 SELECT p.plan::text FROM visual_plans p JOIN visual_runtime r ON r.workspace_id = p.workspace_id AND r.plan_id = p.id
 WHERE r.workspace_id = @workspace_id AND r.environment_id = sqlc.arg(environment_id)::uuid
 UNION ALL
 SELECT p.plan::text FROM visual_plans p JOIN cross_host_runtime r ON r.workspace_id = p.workspace_id AND r.plan_id = p.id
 WHERE r.workspace_id = @workspace_id AND r.environment_id = sqlc.arg(environment_id)::uuid
 UNION ALL
 SELECT o.command::text FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.environment_id = sqlc.arg(environment_id)::uuid
  AND o.state IN ('queued','dispatched','running','needs_attention')
 UNION ALL
 SELECT u.plan::text FROM supported_upgrades u WHERE u.workspace_id = @workspace_id AND u.environment_id = sqlc.arg(environment_id)::uuid
  AND u.status->>'phase' NOT IN ('rejected','complete','returned')
)
SELECT EXISTS(SELECT 1 FROM documents d, versions v WHERE strpos(d.body, v.id) > 0)::boolean;

-- name: RetireSecret :exec
UPDATE secrets SET retired_at = now() WHERE workspace_id = @workspace_id AND id = @id;

-- name: EraseRecoveredSecretVersion :exec
-- Recovery takes a checkpoint's erasure of a version; an erasure is never undone.
UPDATE secret_versions SET encrypted_value = NULL,erased_at = sqlc.arg(erased_at)::timestamptz
WHERE workspace_id = @workspace_id AND id = @id AND erased_at IS NULL;

-- name: EraseSecretValues :exec
UPDATE secret_versions SET encrypted_value = NULL,erased_at = now()
WHERE workspace_id = @workspace_id AND secret_id = @secret_id AND erased_at IS NULL;
