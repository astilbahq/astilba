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
    retired_bearer_hash,consumed_code_hash,key_rotated_at,rotate_key_requested
FROM servers WHERE id = @server_id FOR UPDATE;

-- name: BearerKnown :one
-- unscoped: an unlocked check that a bind's bearer is this server's, or the one its bind retired,
-- before anything takes the row lock. A stranger's guess costs nothing.
SELECT COALESCE(credential_hash = @bearer_hash OR retired_bearer_hash = @bearer_hash,false)::boolean
FROM servers WHERE id = @server_id;

-- name: CodeTakenByAnotherKey :one
-- unscoped: an unlocked check before anything takes the row lock.
SELECT COALESCE(consumed_code_hash = @code_hash AND credential_hash IS NOT NULL AND credential_hash <> @candidate,false)::boolean
FROM servers WHERE id = @server_id;

-- name: RetiredBearerKnown :one
-- unscoped: an unlocked check that a refused bearer is the one this server's bind retired.
SELECT COALESCE(retired_bearer_hash = @bearer_hash AND credential_hash IS NOT NULL,false)::boolean
FROM servers WHERE id = @server_id;

-- name: LockRetiredBearer :one
-- unscoped: reached only with the bearer this server's bind retired. The window is read from the
-- database clock: from two minutes after the bound key's first use, longer than any request takes,
-- until a day after the bind.
SELECT s.workspace_id,s.enrollment_epoch,
    COALESCE(s.retired_bearer_hash = @bearer_hash AND s.credential_hash IS NOT NULL,false)::boolean AS retired,
    COALESCE(k.first_used_at + interval '2 minutes' < now() AND s.bound_at + interval '24 hours' > now(),false)::boolean AS in_window
FROM servers s LEFT JOIN agent_keys k ON k.server_id = s.id AND k.key_hash = s.credential_hash
WHERE s.id = @server_id FOR UPDATE OF s;

-- name: ConsumeAgentNonce :execrows
INSERT INTO agent_nonces_consumed(nonce_hash,expires_at) VALUES(@nonce_hash,@expires_at) ON CONFLICT DO NOTHING;

-- name: PruneAgentNonces :exec
-- unscoped: retention covers every workspace, and a consumed nonce is needed only until it expires.
DELETE FROM agent_nonces_consumed WHERE nonce_hash IN (
    SELECT nonce_hash FROM agent_nonces_consumed WHERE expires_at < now() ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED);

-- name: CounterConflict :one
-- Proof of a second holder: one holder takes a nonce, then its next counter, so its counters rise
-- with its nonces. A value at or above this one with an older nonce, one at or below it with a newer
-- nonce, or this value with a different nonce, came from someone else.
SELECT EXISTS(SELECT 1 FROM agent_counter_values a
    WHERE a.workspace_id = sqlc.arg(workspace_id)::uuid AND a.server_id = sqlc.arg(server_id)::uuid AND a.epoch = sqlc.arg(epoch)::bigint
      AND ((a.value >= sqlc.arg(value)::bigint AND a.nonce_seq < sqlc.arg(nonce_seq)::bigint)
        OR (a.value <= sqlc.arg(value)::bigint AND a.nonce_seq > sqlc.arg(nonce_seq)::bigint)
        OR (a.value = sqlc.arg(value)::bigint AND a.nonce_hash <> sqlc.arg(nonce_hash)::bytea)))::boolean AS conflict,
    EXISTS(SELECT 1 FROM agent_counter_values b
    WHERE b.workspace_id = sqlc.arg(workspace_id)::uuid AND b.server_id = sqlc.arg(server_id)::uuid AND b.epoch = sqlc.arg(epoch)::bigint AND b.value >= sqlc.arg(value)::bigint)::boolean AS stale;

-- name: RecordAgentCounter :exec
INSERT INTO agent_counter_values(workspace_id,server_id,epoch,value,nonce_seq,nonce_hash) VALUES(@workspace_id,@server_id,@epoch,@value,@nonce_seq,@nonce_hash)
ON CONFLICT DO NOTHING;

-- name: PruneAgentCounters :exec
DELETE FROM agent_counter_values c WHERE c.workspace_id = sqlc.arg(workspace_id)::uuid AND c.server_id = sqlc.arg(server_id)::uuid
    AND (c.epoch <> sqlc.arg(epoch)::bigint OR c.nonce_seq < (SELECT COALESCE(min(r.nonce_seq),0) FROM (
        SELECT v.nonce_seq FROM agent_counter_values v WHERE v.workspace_id = sqlc.arg(workspace_id)::uuid AND v.server_id = sqlc.arg(server_id)::uuid AND v.epoch = sqlc.arg(epoch)::bigint
        ORDER BY v.nonce_seq DESC LIMIT 256) r));

-- name: SupersedeAgentLineages :exec
UPDATE agent_lineages SET superseded_at = now()
WHERE workspace_id = @workspace_id AND server_id = @server_id AND superseded_at IS NULL;

-- name: InsertAgentLineage :exec
-- Token lifetimes run on the database clock, as the checks that read them do.
INSERT INTO agent_lineages(id,workspace_id,server_id,key_hash,epoch,refresh_hash,token_hash,token_expires_at)
VALUES(@id,@workspace_id,@server_id,@key_hash,@epoch,@refresh_hash,@token_hash,now() + make_interval(secs => sqlc.arg(lifetime_seconds)::int));

-- name: PruneAgentLineages :exec
-- A superseded session is kept a day, so a refused request can still be traced to it.
DELETE FROM agent_lineages
WHERE workspace_id = @workspace_id AND server_id = @server_id AND superseded_at < now() - interval '24 hours';

-- name: PruneAgentIdentityHistory :exec
-- unscoped: retention covers every workspace. The keys and events of earlier epochs are kept until
-- 30 days after the latest epoch ended, and while a conflict is current, so a copy presenting a
-- replaced key is still recorded and the owner can still see what raised the conflict. Events of
-- the current epoch are bounded, since refusals and conflicts are recorded once each.
WITH due AS (
    SELECT id,workspace_id,enrollment_epoch FROM servers
    WHERE identity_history_until < now() AND NOT COALESCE(identity_conflict_epoch = enrollment_epoch,false)
    ORDER BY identity_history_until LIMIT 100 FOR UPDATE SKIP LOCKED
), keys AS (
    DELETE FROM agent_keys k USING due WHERE k.workspace_id = due.workspace_id AND k.server_id = due.id AND k.epoch < due.enrollment_epoch
), events AS (
    DELETE FROM agent_identity_events e USING due WHERE e.workspace_id = due.workspace_id AND e.server_id = due.id AND e.epoch < due.enrollment_epoch
)
UPDATE servers s SET identity_history_until = NULL FROM due WHERE s.id = due.id;

-- name: MarkAgentKeyUsed :exec
-- A key's first use is the moment its predecessors' holders, and a bind's retired bearer, can
-- only be someone else.
UPDATE agent_keys SET first_used_at = COALESCE(first_used_at,now())
WHERE workspace_id = @workspace_id AND server_id = @server_id AND key_hash = @key_hash;

-- name: MarkSuccessorSeen :exec
UPDATE agent_keys SET successor_seen_seq = @seq
WHERE workspace_id = @workspace_id AND server_id = @server_id AND epoch = @epoch AND superseded_at IS NOT NULL AND successor_seen_seq IS NULL;

-- name: RefreshLineage :one
-- unscoped: the refresh secret establishes the workspace.
SELECT l.id,l.workspace_id,l.key_hash,l.epoch,l.superseded_at,k.public_key
FROM agent_lineages l JOIN agent_keys k ON k.server_id = l.server_id AND k.key_hash = l.key_hash
WHERE l.server_id = @server_id AND l.refresh_hash = @refresh_hash;

-- name: RotateLineageTokens :execrows
-- Only the current secret renews a live session, once. The previous token stays valid until it
-- expires, so requests in flight across a refresh succeed.
UPDATE agent_lineages SET
    refresh_hash = @refresh_hash,
    previous_token_hash = token_hash,previous_token_expires_at = token_expires_at,
    token_hash = @token_hash,token_expires_at = now() + make_interval(secs => sqlc.arg(lifetime_seconds)::int)
WHERE workspace_id = @workspace_id AND id = @id AND superseded_at IS NULL AND refresh_hash = @presented;

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
ON CONFLICT (server_id,key_hash) DO UPDATE SET epoch = EXCLUDED.epoch,superseded_at = NULL,successor_seen_seq = NULL,first_used_at = NULL;

-- name: SupersedeAgentKey :exec
UPDATE agent_keys SET superseded_at = now() WHERE workspace_id = @workspace_id AND server_id = @server_id AND key_hash = @key_hash;

-- name: SetServerKey :exec
UPDATE servers SET credential_hash = @key_hash,previous_hash = NULL,previous_until = NULL,
    key_rotated_at = now(),rotate_key_requested = false
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: BindServerKey :exec
-- Binding retires the bearer and starts a new epoch. A conflict raised in the epoch it ends stays
-- current: only a one-time code clears one.
UPDATE servers SET credential_hash = @key_hash,previous_hash = NULL,previous_until = NULL,
    retired_bearer_hash = @bearer_hash,bound_at = now(),key_rotated_at = now(),rotation_requested = false,
    rotate_key_requested = false,enrollment_epoch = enrollment_epoch + 1,identity_history_until = now() + interval '30 days',
    identity_conflict_epoch = CASE WHEN identity_conflict_epoch = enrollment_epoch THEN enrollment_epoch + 1 ELSE identity_conflict_epoch END
WHERE workspace_id = @workspace_id AND id = @server_id;

-- name: StartNewEpoch :one
-- Consuming a one-time code is the only thing besides a bind that starts an epoch, and the only
-- thing that clears an identity conflict.
UPDATE servers SET enrollment_epoch = enrollment_epoch + 1,consumed_code_hash = @code_hash,
    identity_history_until = now() + interval '30 days',
    retired_bearer_hash = NULL,bound_at = NULL,
    identity_conflict_at = NULL,identity_conflict_epoch = NULL,identity_conflict = NULL,
    key_rotated_at = CASE WHEN @key_bound::boolean THEN now() ELSE NULL END,rotate_key_requested = false
WHERE workspace_id = @workspace_id AND id = @server_id
RETURNING enrollment_epoch;

-- name: RecordIdentityConflict :exec
-- The first conflict of an epoch is kept; one from an earlier epoch is replaced.
UPDATE servers SET
    identity_conflict = CASE WHEN identity_conflict_epoch = enrollment_epoch THEN identity_conflict ELSE @detail END,
    identity_conflict_at = CASE WHEN identity_conflict_epoch = enrollment_epoch THEN identity_conflict_at ELSE now() END,
    identity_conflict_epoch = enrollment_epoch
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
SELECT EXISTS(SELECT 1 FROM troubleshooting_sessions WHERE workspace_id = @workspace_id AND server_id = @server_id AND state IN ('offered','active'))::boolean;

-- name: ServerIdentities :many
-- A server has at most one live lineage. Its key is the newest of the current epoch, which stays
-- known while revocation, a new code or fencing has cleared the credential. A conflict is shown only
-- in the epoch it was raised in, and not once the server is revoked.
SELECT s.id,s.key_rotated_at,s.rotate_key_requested,s.revoked,s.identity_conflict_at,s.identity_conflict,s.identity_conflict_epoch,s.enrollment_epoch,
    k.created_at AS key_created_at,l.created_at AS lineage_started_at
FROM servers s
LEFT JOIN agent_keys k ON k.server_id = s.id AND k.epoch = s.enrollment_epoch
    AND NOT EXISTS(SELECT 1 FROM agent_keys n WHERE n.server_id = k.server_id AND n.epoch = k.epoch AND n.created_at > k.created_at)
LEFT JOIN agent_lineages l ON l.server_id = s.id AND l.superseded_at IS NULL
WHERE s.workspace_id = sqlc.arg(workspace_id)::uuid;

-- name: IdentityConflicts :many
-- An identity conflict is current only in the epoch it was raised in.
SELECT id,identity_conflict_at FROM servers
WHERE workspace_id = @workspace_id AND identity_conflict_at IS NOT NULL AND identity_conflict_epoch = enrollment_epoch;
