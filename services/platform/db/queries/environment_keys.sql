-- name: EnvironmentKeys :many
-- A workspace's environment keys, active and destroyed, which a session opens environment values
-- with.
SELECT environment_id, generation, state, workspace_generation, wrapped, kcv
FROM environment_keys WHERE workspace_id = @workspace_id;

-- name: ShareEnvironmentKey :one
-- Holds an environment's key at generation, while it is active, until the transaction ends, so a
-- purge waits for a value kept under it to be stored, and the value is never stored after it.
SELECT generation FROM environment_keys
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND generation = @generation AND state = 'active'
FOR SHARE;

-- name: DestroyEnvironmentKeys :many
-- Destroys an environment's keys, keeping each row without its key.
UPDATE environment_keys SET state = 'destroyed', wrapped = NULL, kcv = NULL
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND state = 'active'
RETURNING generation;

-- name: EndedEnvironmentKeys :many
-- The environments with an active key that has no further use: purging or purged, or a preview
-- whose every request has ended. Recovery can bring either about, by taking a checkpoint's purge
-- or ending a request.
SELECT DISTINCT k.environment_id, (e.state IN ('purging', 'purged'))::boolean AS purged FROM environment_keys k
JOIN environments e ON e.workspace_id = k.workspace_id AND e.id = k.environment_id
WHERE k.workspace_id = @workspace_id AND k.state = 'active' AND (e.state IN ('purging', 'purged') OR e.purpose = 'preview'
    AND NOT EXISTS (SELECT 1 FROM preview_requests r WHERE r.workspace_id = e.workspace_id AND r.environment_id = e.id AND r.state <> 'deleted'));
