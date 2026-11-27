-- +goose Up
-- astilba: not additive: an older release can't read the backup_account alert kind, and would deliver a managed destination's empty access.
-- Management-minted backup storage keys. A storage account is one AWS account and the admin
-- credential management mints with; a managed destination takes its keys from one. Each key is an
-- IAM user limited to one destination's repositories, held by one server, and revoked when that
-- server is revoked. See docs/backups.md#managed-keys.
CREATE TABLE backup_storage_accounts (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 -- Fixed for the account's life, from sts:GetCallerIdentity: a replacement credential must be the
 -- same account's.
 aws_account text NOT NULL CHECK(aws_account ~ '^[0-9]{12}$'),
 admin_arn text NOT NULL CHECK(length(admin_arn) <= 2048),
 boundary text NOT NULL CHECK(length(boundary) <= 2048),
 -- The admin credential, sealed under backup-storage-account. Raised each time it is replaced.
 encrypted_credential bytea NOT NULL, generation bigint NOT NULL DEFAULT 1 CHECK(generation >= 1),
 -- Installations that share the account after a workspace transfer, whose users the check
 -- shows without an alert.
 peers uuid[] NOT NULL DEFAULT '{}',
 -- The latest account check: its time, a fixed error code, and what it found, as stable JSON.
 checked_at timestamptz, check_error text CHECK(length(check_error) <= 80),
 report jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(report::text) <= 262144),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id), UNIQUE(workspace_id,aws_account)
);

ALTER TABLE backup_destinations ADD COLUMN account_id uuid,
 ADD COLUMN remint_attempts integer NOT NULL DEFAULT 0, ADD COLUMN remint_next_at timestamptz,
 ADD COLUMN remint_error text CHECK(length(remint_error) <= 80),
 ADD FOREIGN KEY(workspace_id,account_id) REFERENCES backup_storage_accounts(workspace_id,id),
 ADD CONSTRAINT backup_destination_managed CHECK(account_id IS NULL OR (configuration->>'kind' = 's3' AND server_id IS NOT NULL AND NOT shared));

-- How many times a server has been revoked. A handover is bound to one revocation, and merges keep
-- the higher count, so taking an earlier copy's revoked_at doesn't break it.
ALTER TABLE servers ADD COLUMN revocations bigint NOT NULL DEFAULT 0;
UPDATE servers SET revocations = 1 WHERE revoked;

ALTER TABLE backup_recoveries ADD UNIQUE(workspace_id,id);

CREATE TABLE backup_storage_keys (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, account_id uuid NOT NULL,
 destination_id uuid, holder_server_id uuid,
 purpose text NOT NULL CHECK(purpose IN ('server','recovery','found')),
 -- A recovery key's start: the request key a replay carries, the policy and snapshot it restores,
 -- and the recovery once it exists.
 recovery_request text CHECK(length(recovery_request) BETWEEN 1 AND 128), recovery_policy_id uuid,
 recovery_snapshot text CHECK(recovery_snapshot ~ '^[a-f0-9]{64}$'), recovery_id uuid UNIQUE,
 scope text CHECK(length(scope) BETWEEN 1 AND 512 AND scope LIKE '%/'),
 user_name text NOT NULL UNIQUE CHECK(user_name ~ '^[A-Za-z0-9+=,.@_-]{1,64}$'),
 access_key_id text CHECK(access_key_id ~ '^[A-Z0-9]{16,128}$'),
 state text NOT NULL DEFAULT 'minting' CHECK(state IN ('minting','active','revoking','revoked')),
 mint_deadline timestamptz NOT NULL DEFAULT clock_timestamp() + interval '5 minutes',
 -- The holder's revocation count at the revocation that handed the key over to a transfer.
 handed_over bigint,
 activated_at timestamptz, revoked_at timestamptz,
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(), attempts integer NOT NULL DEFAULT 0,
 last_error text CHECK(length(last_error) <= 80),
 -- The policy generations whose install carries this key. An older server key is superseded only
 -- once the holder reports them all.
 installs jsonb NOT NULL DEFAULT '[]' CHECK(octet_length(installs::text) <= 65536),
 -- A found user's tags as the account check saw them.
 found_tags jsonb CHECK(octet_length(found_tags::text) <= 8192),
 -- The minted secret, sealed under backup-storage-key once the key is active.
 encrypted_secret bytea,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,account_id) REFERENCES backup_storage_accounts(workspace_id,id),
 FOREIGN KEY(workspace_id,destination_id) REFERENCES backup_destinations(workspace_id,id),
 FOREIGN KEY(workspace_id,holder_server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,recovery_id) REFERENCES backup_recoveries(workspace_id,id),
 CHECK(CASE purpose
  WHEN 'found' THEN destination_id IS NULL AND holder_server_id IS NULL AND scope IS NULL AND encrypted_secret IS NULL AND activated_at IS NULL AND found_tags IS NOT NULL AND state IN ('revoking','revoked')
  ELSE destination_id IS NOT NULL AND holder_server_id IS NOT NULL AND scope IS NOT NULL AND found_tags IS NULL END),
 CHECK((purpose = 'recovery') = (recovery_request IS NOT NULL AND recovery_policy_id IS NOT NULL AND recovery_snapshot IS NOT NULL)),
 CHECK(recovery_id IS NULL OR purpose = 'recovery'),
 CHECK(handed_over IS NULL OR purpose = 'server'),
 CHECK((activated_at IS NULL) = (encrypted_secret IS NULL) AND (activated_at IS NULL) = (access_key_id IS NULL OR purpose = 'found')),
 CHECK((state = 'minting') <= (activated_at IS NULL)),
 CHECK((state = 'revoked') = (revoked_at IS NOT NULL))
);
-- One start of a recovery at a time per request key.
CREATE UNIQUE INDEX backup_storage_keys_recovery_request ON backup_storage_keys(workspace_id,recovery_request) WHERE purpose = 'recovery' AND state IN ('minting','active');
CREATE INDEX backup_storage_keys_destination ON backup_storage_keys(workspace_id,destination_id,created_at) WHERE destination_id IS NOT NULL;
CREATE INDEX backup_storage_keys_holder ON backup_storage_keys(workspace_id,holder_server_id) WHERE holder_server_id IS NOT NULL;
CREATE INDEX backup_storage_keys_due ON backup_storage_keys(next_attempt_at) WHERE state = 'revoking';

ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held','backup_exposure','backup_account'));

-- +goose Down
DELETE FROM operational_alerts WHERE kind = 'backup_account';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held','backup_exposure'));
DROP TABLE backup_storage_keys;
ALTER TABLE backup_recoveries DROP CONSTRAINT backup_recoveries_workspace_id_id_key;
ALTER TABLE servers DROP COLUMN revocations;
ALTER TABLE backup_destinations DROP CONSTRAINT backup_destination_managed, DROP COLUMN account_id,
 DROP COLUMN remint_attempts, DROP COLUMN remint_next_at, DROP COLUMN remint_error;
DROP TABLE backup_storage_accounts;
