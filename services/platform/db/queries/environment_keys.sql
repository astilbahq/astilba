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

-- name: EnvironmentKeyForSealing :one
-- An environment's active key, held until the transaction ends so a purge waits for the value
-- sealed under it to be stored, and none is stored after the purge.
SELECT generation, workspace_generation, wrapped, kcv FROM environment_keys
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND state = 'active'
FOR SHARE;

-- name: EnvironmentSealingRefused :one
-- Whether an environment of the workspace may no longer have values sealed for it: it is being
-- purged, or its key was destroyed. No row is an environment the workspace does not have.
SELECT (e.state IN ('purging', 'purged') OR EXISTS (
    SELECT 1 FROM environment_keys k WHERE k.environment_id = e.id AND k.state = 'destroyed'))::boolean AS refused
FROM environments e WHERE e.workspace_id = @workspace_id AND e.id = @environment_id;

-- name: InsertEnvironmentKey :execrows
-- An environment's first key, made by whichever writer seals its first value; another made at
-- the same time is kept instead.
INSERT INTO environment_keys(workspace_id, environment_id, generation, state, workspace_generation, wrapped, kcv)
VALUES (@workspace_id, @environment_id, @generation, 'active', @workspace_generation, @wrapped, @kcv)
ON CONFLICT (environment_id, generation) DO NOTHING;

-- name: LockEnvironmentKeyCreation :exec
-- Holds the making of an environment's first key, named "environment:<workspace>:<environment>",
-- until the transaction ends, so writers making it at once take turns, and the second finds the
-- first's.
SELECT pg_advisory_xact_lock(hashtextextended(@name::text, 4281642306));

-- name: LockEnvironmentKeys :exec
-- Holds an environment's keys against writers until the transaction ends, before a purge locks
-- the environment's values: a writer holds the key, then its value, so the purge takes them in the
-- same order.
SELECT 1 FROM environment_keys WHERE workspace_id = @workspace_id AND environment_id = @environment_id FOR UPDATE;
