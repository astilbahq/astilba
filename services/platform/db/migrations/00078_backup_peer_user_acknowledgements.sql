-- +goose Up
-- astilba: not additive: an older release can't read the backup_peer_user alert kind.
-- The IAM users a storage account's owner has acknowledged as a peer installation's. The account
-- check shows a user tagged with a peer's installation as that peer's, and a leaked admin key can
-- make one, so each raises backup_peer_user until the owner acknowledges it by name. An
-- acknowledgement holds the user's IAM user ID and the access keys it had when the owner looked:
-- a user made again under the same name, or given another access key, raises the alert again.
-- id is the found user's identity, derived from the account, the user's name and its IAM user ID,
-- which the alert also names. Rows are this installation's own judgement, so exports leave them
-- out, and a check that lists the account's users in full deletes those of users it no longer
-- finds.
CREATE TABLE backup_storage_acknowledgements (
 workspace_id uuid NOT NULL, id uuid NOT NULL, account_id uuid NOT NULL,
 user_name text NOT NULL CHECK(length(user_name) BETWEEN 1 AND 64),
 iam_user_id text NOT NULL CHECK(length(iam_user_id) BETWEEN 1 AND 128),
 access_key_ids text[] NOT NULL CHECK(cardinality(access_key_ids) <= 10),
 acknowledged_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,account_id) REFERENCES backup_storage_accounts(workspace_id,id)
);
CREATE INDEX backup_storage_acknowledgements_account ON backup_storage_acknowledgements(workspace_id,account_id);

ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held','backup_exposure','backup_account','backup_peer_user'));

-- Reports written before this release don't carry the IAM user IDs and access keys a peer's user
-- is acknowledged by, so every account is checked again at once.
UPDATE backup_storage_accounts SET checked_at = NULL;

-- +goose Down
DELETE FROM operational_alerts WHERE kind = 'backup_peer_user';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held','backup_exposure','backup_account'));
DROP TABLE backup_storage_acknowledgements;
