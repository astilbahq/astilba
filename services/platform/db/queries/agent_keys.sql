-- name: InstallationSecret :one
SELECT secret FROM installation_secrets WHERE name = @name;

-- name: InsertInstallationSecret :exec
INSERT INTO installation_secrets(name,secret) VALUES(@name,@secret) ON CONFLICT (name) DO NOTHING;

-- name: NextAgentNonce :one
-- The sequence orders nonces across replicas without trusting any clock.
SELECT nextval('agent_nonce_seq')::bigint AS seq,(extract(epoch FROM now())*1000)::bigint AS now_ms;

-- name: DatabaseNowMillis :one
SELECT (extract(epoch FROM now())*1000)::bigint AS now_ms;

-- name: AgentKey :one
-- unscoped: a signature by a key this server holds establishes the workspace.
SELECT workspace_id,public_key,epoch,first_used_at,superseded_at,successor_seen_seq
FROM agent_keys WHERE server_id = @server_id AND key_hash = @key_hash;

-- name: LockServerIdentity :one
-- unscoped: the caller has proved possession of a key or credential of this server.
SELECT workspace_id,role,preview_only,machine_id,revoked,awaiting_transfer,credential_hash,enrollment_epoch,
    retired_bearer_hash,bound_at,start_counter_floor,start_counter_floor_seq,key_rotated_at,rotate_key_requested,
    identity_conflict_epoch,
    COALESCE((capabilities->>'credentialFreeVisualCancel')::boolean,false)::boolean AS credential_free_visual_cancel
FROM servers WHERE id = @server_id FOR UPDATE;

-- name: ConsumeAgentNonce :execrows
INSERT INTO agent_nonces_consumed(nonce_hash,expires_at) VALUES(@nonce_hash,@expires_at) ON CONFLICT DO NOTHING;

-- name: PruneAgentNonces :exec
DELETE FROM agent_nonces_consumed WHERE expires_at < now();

-- name: CounterConflict :one
-- Proof of a second holder: one holder's counters only rise with its nonces, so a value at or above
-- this one with an older nonce, or this value with a different nonce, came from someone else.
SELECT EXISTS(SELECT 1 FROM agent_counter_values a
    WHERE a.server_id = sqlc.arg(server_id)::uuid AND a.epoch = sqlc.arg(epoch)::bigint
      AND ((a.value >= sqlc.arg(value)::bigint AND a.nonce_seq < sqlc.arg(nonce_seq)::bigint)
        OR (a.value = sqlc.arg(value)::bigint AND a.nonce_hash <> sqlc.arg(nonce_hash)::bytea)))::boolean AS conflict,
    EXISTS(SELECT 1 FROM agent_counter_values b
    WHERE b.server_id = sqlc.arg(server_id)::uuid AND b.epoch = sqlc.arg(epoch)::bigint AND b.value >= sqlc.arg(value)::bigint)::boolean AS stale;

-- name: RecordAgentCounter :exec
INSERT INTO agent_counter_values(server_id,epoch,value,nonce_seq,nonce_hash) VALUES(@server_id,@epoch,@value,@nonce_seq,@nonce_hash)
ON CONFLICT DO NOTHING;

-- name: PruneAgentCounters :exec
DELETE FROM agent_counter_values c WHERE c.server_id = sqlc.arg(server_id)::uuid
    AND (c.epoch <> sqlc.arg(epoch)::bigint OR c.nonce_seq < (SELECT COALESCE(min(r.nonce_seq),0) FROM (
        SELECT v.nonce_seq FROM agent_counter_values v WHERE v.server_id = sqlc.arg(server_id)::uuid AND v.epoch = sqlc.arg(epoch)::bigint
        ORDER BY v.nonce_seq DESC LIMIT 256) r));

-- name: SupersedeAgentLineages :exec
UPDATE agent_lineages SET superseded_at = now() WHERE server_id = @server_id AND superseded_at IS NULL;

-- name: InsertAgentLineage :exec
INSERT INTO agent_lineages(id,workspace_id,server_id,key_hash,epoch,refresh_hash,token_hash,token_expires_at)
VALUES(@id,@workspace_id,@server_id,@key_hash,@epoch,@refresh_hash,@token_hash,@token_expires_at);

-- name: PruneAgentLineages :exec
DELETE FROM agent_lineages WHERE server_id = @server_id AND superseded_at < now() - interval '24 hours';

-- name: MarkAgentKeyUsed :exec
-- A key's first use is the moment its predecessors' holders, and a bind's retired bearer, can
-- only be someone else.
UPDATE agent_keys SET first_used_at = COALESCE(first_used_at,now()) WHERE server_id = @server_id AND key_hash = @key_hash;

-- name: MarkSuccessorSeen :exec
UPDATE agent_keys SET successor_seen_seq = @seq
WHERE server_id = @server_id AND epoch = @epoch AND superseded_at IS NOT NULL AND successor_seen_seq IS NULL;

-- name: RefreshLineage :one
-- unscoped: the refresh secret establishes the workspace.
SELECT l.id,l.workspace_id,l.key_hash,l.epoch,l.superseded_at,
    (l.refresh_hash = @refresh_hash)::boolean AS current,
    (l.previous_refresh_hash = @refresh_hash AND l.previous_refresh_until > now())::boolean AS grace,
    k.public_key
FROM agent_lineages l JOIN agent_keys k ON k.server_id = l.server_id AND k.key_hash = l.key_hash
WHERE l.server_id = @server_id AND (l.refresh_hash = @refresh_hash OR l.previous_refresh_hash = @refresh_hash);

-- name: RotateLineageTokens :execrows
-- The previous token stays valid until it expires, so requests in flight across a refresh succeed.
UPDATE agent_lineages SET
    previous_refresh_hash = CASE WHEN refresh_hash = @presented THEN refresh_hash ELSE previous_refresh_hash END,
    previous_refresh_until = CASE WHEN refresh_hash = @presented THEN now() + interval '60 seconds' ELSE previous_refresh_until END,
    refresh_hash = @refresh_hash,
    previous_token_hash = token_hash, previous_token_expires_at = token_expires_at,
    token_hash = @token_hash, token_expires_at = @token_expires_at
WHERE id = @id AND superseded_at IS NULL;

-- name: AuthenticateAgentToken :one
-- unscoped: the presented token establishes the workspace. A token is live only while its lineage
-- is the server's current one, in the current epoch, for the server's current key.
SELECT s.workspace_id,s.role,l.id AS lineage,l.key_hash,
    COALESCE((s.capabilities->>'credentialFreeVisualCancel')::boolean,false)::boolean AS credential_free_visual_cancel
FROM agent_lineages l JOIN servers s ON s.id = l.server_id AND s.workspace_id = l.workspace_id
WHERE l.server_id = @server_id AND l.superseded_at IS NULL AND l.epoch = s.enrollment_epoch
    AND s.credential_hash = l.key_hash AND NOT s.revoked AND NOT s.awaiting_transfer
    AND ((l.token_hash = @token_hash AND l.token_expires_at > now())
        OR (l.previous_token_hash = @token_hash AND l.previous_token_expires_at > now()));

-- name: InsertAgentKey :exec
INSERT INTO agent_keys(workspace_id,server_id,key_hash,public_key,epoch)
VALUES(@workspace_id,@server_id,@key_hash,@public_key,@epoch)
ON CONFLICT (server_id,key_hash) DO UPDATE SET epoch = EXCLUDED.epoch,superseded_at = NULL,superseded_seq = NULL,successor_seen_seq = NULL,first_used_at = NULL;

-- name: SupersedeAgentKey :exec
UPDATE agent_keys SET superseded_at = now(),superseded_seq = @seq WHERE server_id = @server_id AND key_hash = @key_hash;

-- name: SetServerKey :exec
UPDATE servers SET credential_hash = @key_hash,previous_hash = NULL,previous_until = NULL,
    key_rotated_at = now(),rotate_key_requested = false
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: BindServerKey :exec
-- Binding retires the bearer and starts a new epoch.
UPDATE servers SET credential_hash = @key_hash,previous_hash = NULL,previous_until = NULL,
    retired_bearer_hash = @bearer_hash,bound_at = now(),key_rotated_at = now(),rotation_requested = false,
    rotate_key_requested = false,enrollment_epoch = enrollment_epoch + 1,
    start_counter_floor = NULL,start_counter_floor_seq = NULL
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: BoundKeyFirstUse :one
SELECT k.first_used_at FROM servers s JOIN agent_keys k ON k.server_id = s.id AND k.key_hash = s.credential_hash
WHERE s.id = @server_id;

-- name: RetiredBearer :one
-- unscoped: only compared with the bearer the caller presented, and read only to raise a conflict.
SELECT s.workspace_id,s.retired_bearer_hash,s.bound_at,s.enrollment_epoch,s.credential_hash,k.first_used_at
FROM servers s LEFT JOIN agent_keys k ON k.server_id = s.id AND k.key_hash = s.credential_hash
WHERE s.id = @server_id;

-- name: StartNewEpoch :one
-- Consuming a one-time code is the only thing besides a bind that starts an epoch, and the only
-- thing that clears an identity conflict.
UPDATE servers SET enrollment_epoch = enrollment_epoch + 1,consumed_code_hash = @code_hash,
    retired_bearer_hash = NULL,bound_at = NULL,start_counter_floor = NULL,start_counter_floor_seq = NULL,
    identity_conflict_at = NULL,identity_conflict_epoch = NULL,identity_conflict = NULL,
    key_rotated_at = CASE WHEN @key_bound::boolean THEN now() ELSE NULL END,rotate_key_requested = false
WHERE workspace_id = @workspace_id AND id = @server_id
RETURNING enrollment_epoch;

-- name: RecordIdentityConflict :exec
UPDATE servers SET identity_conflict_at = COALESCE(identity_conflict_at,now()),identity_conflict_epoch = enrollment_epoch,
    identity_conflict = COALESCE(identity_conflict,@detail)
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: FenceServerIdentity :exec
UPDATE servers SET credential_hash = NULL,previous_hash = NULL,previous_until = NULL
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: InsertAgentIdentityEvent :exec
INSERT INTO agent_identity_events(workspace_id,id,server_id,epoch,kind,signal,key_hash,source)
VALUES(@workspace_id,@id,@server_id,@epoch,@kind,@signal,@key_hash,@source)
ON CONFLICT DO NOTHING;

-- name: RequestKeyRotation :one
UPDATE servers s SET rotate_key_requested = true
WHERE s.workspace_id = sqlc.arg(workspace_id)::uuid AND s.id = sqlc.arg(id)::uuid AND NOT s.revoked AND s.credential_hash IS NOT NULL
RETURNING EXISTS(SELECT 1 FROM agent_keys k WHERE k.server_id = s.id AND k.key_hash = s.credential_hash)::boolean AS key_bound;

-- name: TroubleshootingAttached :one
SELECT EXISTS(SELECT 1 FROM troubleshooting_sessions WHERE server_id = @server_id AND state IN ('offered','active'))::boolean;

-- name: ServerIdentities :many
-- A server has at most one live lineage. A conflict is shown only in the epoch it was raised in.
SELECT s.id,s.key_rotated_at,s.identity_conflict_at,s.identity_conflict,s.identity_conflict_epoch,s.enrollment_epoch,
    k.created_at AS key_created_at,l.created_at AS lineage_started_at
FROM servers s
LEFT JOIN agent_keys k ON k.server_id = s.id AND k.key_hash = s.credential_hash
LEFT JOIN agent_lineages l ON l.server_id = s.id AND l.superseded_at IS NULL
WHERE s.workspace_id = sqlc.arg(workspace_id)::uuid;

-- name: IdentityConflicts :many
-- An identity conflict is current only in the epoch it was raised in.
SELECT id,identity_conflict_at FROM servers
WHERE workspace_id = @workspace_id AND identity_conflict_at IS NOT NULL AND identity_conflict_epoch = enrollment_epoch;

-- name: ConsumedCode :one
SELECT consumed_code_hash FROM servers WHERE id = @server_id;
