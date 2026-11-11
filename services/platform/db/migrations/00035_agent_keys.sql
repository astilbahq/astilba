-- +goose Up
-- An agent authenticates with a key it holds and short-lived tokens. credential_hash stays the
-- server's current identity: the hash of its key's SPKI, or of its bearer until it binds a key.
ALTER TABLE servers
    ADD COLUMN enrollment_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN key_rotated_at timestamptz,
    ADD COLUMN rotate_key_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN retired_bearer_hash bytea,
    ADD COLUMN bound_at timestamptz,
    ADD COLUMN consumed_code_hash bytea,
    ADD COLUMN start_counter_floor bigint,
    ADD COLUMN start_counter_floor_seq bigint,
    ADD COLUMN identity_conflict_at timestamptz,
    ADD COLUMN identity_conflict_epoch bigint,
    ADD COLUMN identity_conflict jsonb;

-- Every key a server has held in an epoch. A key superseded by rotation in the current epoch is
-- proof of a second holder once its successor has been used.
CREATE TABLE agent_keys (
    workspace_id uuid NOT NULL,
    server_id uuid NOT NULL,
    key_hash bytea NOT NULL,
    public_key bytea NOT NULL CHECK(length(public_key) <= 200),
    epoch bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    first_used_at timestamptz,
    superseded_at timestamptz,
    superseded_seq bigint,
    successor_seen_seq bigint,
    PRIMARY KEY (server_id, key_hash),
    FOREIGN KEY (workspace_id, server_id) REFERENCES servers(workspace_id, id) ON DELETE CASCADE
);

-- One live lineage per server: a process's run of tokens. Refresh secrets and tokens are stored
-- only as hashes.
CREATE TABLE agent_lineages (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    server_id uuid NOT NULL,
    key_hash bytea NOT NULL,
    epoch bigint NOT NULL,
    refresh_hash bytea NOT NULL,
    previous_refresh_hash bytea,
    previous_refresh_until timestamptz,
    token_hash bytea NOT NULL,
    token_expires_at timestamptz NOT NULL,
    previous_token_hash bytea,
    previous_token_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    superseded_at timestamptz,
    FOREIGN KEY (workspace_id, server_id) REFERENCES servers(workspace_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX agent_lineages_token ON agent_lineages(token_hash);
CREATE INDEX agent_lineages_previous_token ON agent_lineages(previous_token_hash) WHERE previous_token_hash IS NOT NULL;
CREATE UNIQUE INDEX agent_lineages_refresh ON agent_lineages(refresh_hash);
CREATE INDEX agent_lineages_server ON agent_lineages(server_id, superseded_at);

-- Start counters seen in an epoch, with the order of the nonce each came with. One holder's
-- counters rise with its nonces, so a pair that doesn't is proof of a second holder.
CREATE TABLE agent_counter_values (
    server_id uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    epoch bigint NOT NULL,
    value bigint NOT NULL,
    nonce_seq bigint NOT NULL,
    nonce_hash bytea NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, epoch, nonce_seq)
);
CREATE INDEX agent_counter_values_value ON agent_counter_values(server_id, epoch, value);

-- Nonces redeemed by a known key's signature, kept until they would have expired anyway.
CREATE TABLE agent_nonces_consumed (
    nonce_hash bytea PRIMARY KEY,
    expires_at timestamptz NOT NULL
);
CREATE SEQUENCE agent_nonce_seq;

-- What happened to a server's agent identity, for its owners: there is no account to attribute
-- these to, so they don't belong in audit_events.
CREATE TABLE agent_identity_events (
    workspace_id uuid NOT NULL,
    id uuid NOT NULL,
    server_id uuid NOT NULL,
    epoch bigint NOT NULL,
    kind text NOT NULL CHECK(kind IN ('bound','enrolled','rotated','refused','conflict','fenced')),
    signal text NOT NULL DEFAULT '' CHECK(length(signal) <= 40),
    key_hash bytea,
    source text NOT NULL DEFAULT '' CHECK(length(source) <= 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, server_id) REFERENCES servers(workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX agent_identity_events_server ON agent_identity_events(server_id, created_at DESC);
CREATE UNIQUE INDEX agent_identity_events_once ON agent_identity_events(server_id, epoch, kind, signal, key_hash)
    WHERE kind = 'refused';

-- A troubleshooting session follows the agent session it attached through, not just its key.
ALTER TABLE troubleshooting_sessions ADD COLUMN agent_lineage uuid;

CREATE TABLE installation_secrets (
    name text PRIMARY KEY CHECK(length(name) <= 40),
    secret bytea NOT NULL CHECK(length(secret) = 32)
);

ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict'));

-- +goose Down
-- Older management authenticates only bearers, so a key-bound server has no credential it can
-- check. Clearing it makes the server need a new enrollment code rather than locking it out.
UPDATE servers s SET credential_hash = NULL, previous_hash = NULL, previous_until = NULL
WHERE EXISTS (SELECT 1 FROM agent_keys k WHERE k.server_id = s.id AND k.key_hash = s.credential_hash);
DELETE FROM operational_alerts WHERE kind = 'identity_conflict';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem'));
DROP TABLE installation_secrets, agent_identity_events, agent_nonces_consumed, agent_counter_values, agent_lineages, agent_keys;
DROP SEQUENCE agent_nonce_seq;
ALTER TABLE troubleshooting_sessions DROP COLUMN agent_lineage;
ALTER TABLE servers
    DROP COLUMN identity_conflict, DROP COLUMN identity_conflict_epoch, DROP COLUMN identity_conflict_at,
    DROP COLUMN start_counter_floor_seq, DROP COLUMN start_counter_floor, DROP COLUMN consumed_code_hash,
    DROP COLUMN bound_at, DROP COLUMN retired_bearer_hash, DROP COLUMN rotate_key_requested,
    DROP COLUMN key_rotated_at, DROP COLUMN enrollment_epoch;
