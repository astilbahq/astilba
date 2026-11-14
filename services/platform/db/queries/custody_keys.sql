-- Custody: workspace and installation keys, the generations a session unwraps, and the instances
-- holding token keys.

-- name: WorkspaceKeyWrappings :many
-- The workspace's generations in the given states, each with its wrapping for one purpose and
-- backend, or none when it has no such wrapping.
SELECT k.generation,k.kcv,w.wrapped FROM workspace_keys k
LEFT JOIN workspace_key_wrappings w ON w.workspace_id = k.workspace_id AND w.generation = k.generation
    AND w.purpose = @purpose AND w.backend_id = @backend_id
WHERE k.workspace_id = @workspace_id AND k.state = ANY(@states::text[])
ORDER BY k.generation;

-- name: InstallationKeyWrappings :many
-- The installation's generations of one scope in the given states, each with its wrapping for one
-- purpose and backend, or none.
SELECT k.generation,k.kcv,w.wrapped FROM installation_keys k
LEFT JOIN installation_key_wrappings w ON w.scope = k.scope AND w.generation = k.generation
    AND w.purpose = @purpose AND w.backend_id = @backend_id
WHERE k.scope = @scope AND k.state = ANY(@states::text[])
ORDER BY k.generation;

-- name: CustodyBackendsVersion :one
SELECT version FROM custody_backends_version;

-- name: ShareCustodyBackendsVersion :one
-- Held until the transaction storing a new key commits, so the backends it was wrapped for cannot
-- change before then.
SELECT version FROM custody_backends_version FOR SHARE;

-- name: WriteBackends :many
-- The backends every new key is wrapped for.
SELECT backend_id FROM custody_backends WHERE role IN ('read+write','write') ORDER BY backend_id;

-- name: InsertWorkspaceKey :exec
INSERT INTO workspace_keys(workspace_id,generation,state,kcv) VALUES(@workspace_id,@generation,'active',@kcv);

-- name: InsertWorkspaceKeyWrapping :exec
INSERT INTO workspace_key_wrappings(workspace_id,generation,purpose,backend_id,wrapped)
VALUES(@workspace_id,@generation,@purpose,@backend_id,@wrapped);

-- name: InsertInstallationKey :exec
INSERT INTO installation_keys(scope,generation,state,kcv) VALUES(@scope,@generation,'active',@kcv);

-- name: InsertInstallationKeyWrapping :exec
INSERT INTO installation_key_wrappings(scope,generation,purpose,backend_id,wrapped)
VALUES(@scope,@generation,@purpose,@backend_id,@wrapped);

-- name: WorkspaceKeyState :one
-- Whether the workspace still exists, and whether it has keys yet.
SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = @workspace_id) AS present,
    EXISTS(SELECT 1 FROM workspace_keys WHERE workspace_id = @workspace_id) AS has_keys;

-- name: InstallationScopeHasKeys :one
SELECT EXISTS(SELECT 1 FROM installation_keys WHERE scope = @scope);

-- name: WorkspacesWithoutKeys :many
-- unscoped: creating keys goes through every workspace that has none, in ID order from after.
SELECT w.id FROM workspaces w
WHERE w.id > @after AND NOT EXISTS(SELECT 1 FROM workspace_keys k WHERE k.workspace_id = w.id)
ORDER BY w.id LIMIT @batch;

-- name: TryLockKeyCreation :one
-- A session lock, so it covers wrapping the key before the transaction that stores it. Workers
-- creating keys skip a holder another has, rather than wrapping a key that would lose.
SELECT pg_try_advisory_lock(hashtextextended(@holder::text, 4281642306));

-- name: UnlockKeyCreation :one
SELECT pg_advisory_unlock(hashtextextended(@holder::text, 4281642306));

-- name: KeyProgress :one
-- unscoped: how far creating keys has got across the installation.
SELECT (SELECT count(*) FROM workspaces)::bigint AS workspaces,
    (SELECT count(DISTINCT workspace_id) FROM workspace_keys)::bigint AS with_keys,
    EXISTS(SELECT 1 FROM installation_keys WHERE scope = 'installation') AS installation,
    EXISTS(SELECT 1 FROM installation_keys WHERE scope = 'token') AS token;

-- name: AdvanceCustodyStage :execrows
-- Records the stage after completed as done, once.
UPDATE custody_stage SET stage = stage + 1, advanced_at = now() WHERE stage = @completed;

-- name: RecordCustodyInstance :exec
INSERT INTO custody_instances(instance_id,token_generations) VALUES(@instance_id,@token_generations)
ON CONFLICT (instance_id) DO UPDATE SET token_generations = EXCLUDED.token_generations, seen_at = now();

-- name: PruneCustodyInstances :exec
-- An instance unseen for a day has stopped.
DELETE FROM custody_instances WHERE seen_at < now() - interval '1 day';

-- name: AllWorkspaceKeyWrappings :many
-- unscoped: a rewrap wraps every workspace key again, across the installation.
SELECT workspace_id,generation,purpose,wrapped FROM workspace_key_wrappings WHERE backend_id = @backend_id
ORDER BY workspace_id,generation,purpose;

-- name: RewrapWorkspaceKeyWrapping :execrows
-- Replaces the wrapping it was read as, so a concurrent rewrap's is never overwritten.
UPDATE workspace_key_wrappings SET wrapped = @wrapped
WHERE workspace_id = @workspace_id AND generation = @generation AND purpose = @purpose AND backend_id = @backend_id AND wrapped = @previous;

-- name: AllInstallationKeyWrappings :many
SELECT scope,generation,purpose,wrapped FROM installation_key_wrappings WHERE backend_id = @backend_id
ORDER BY scope,generation,purpose;

-- name: RewrapInstallationKeyWrapping :execrows
UPDATE installation_key_wrappings SET wrapped = @wrapped
WHERE scope = @scope AND generation = @generation AND purpose = @purpose AND backend_id = @backend_id AND wrapped = @previous;
