-- +goose Up
-- astilba: not additive: an older release can't read the backup_peer_user alert kind.
-- The owner's acknowledgements of IAM users as a peer installation's. The account check shows a
-- user tagged with a peer's installation as that peer's, and a leaked admin key can make one, so
-- each raises backup_peer_user until the owner acknowledges it by name. An acknowledgement holds
-- the user's IAM user ID and the access keys it had when the owner reviewed it: a user made again
-- under the same name, or given another access key, raises the alert again.
-- identity is the found user's, derived from the account, the user's name and its IAM user ID,
-- which the alert also names. Each acknowledgement is its own row, never changed, which the
-- backup.key.acknowledge audit names. Rows are this installation's own judgement, so exports leave
-- them out.
CREATE TABLE backup_storage_acknowledgements (
 workspace_id uuid NOT NULL, id uuid NOT NULL, account_id uuid NOT NULL, identity uuid NOT NULL,
 user_name text NOT NULL CHECK(length(user_name) BETWEEN 1 AND 64),
 iam_user_id text NOT NULL CHECK(length(iam_user_id) BETWEEN 1 AND 128),
 access_key_ids text[] NOT NULL CHECK(cardinality(access_key_ids) <= 10),
 acknowledged_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(workspace_id,id),
 FOREIGN KEY(workspace_id,account_id) REFERENCES backup_storage_accounts(workspace_id,id)
);
CREATE INDEX backup_storage_acknowledgements_identity ON backup_storage_acknowledgements(workspace_id,account_id,identity);

-- A report now holds each found user's identity, IAM user ID and access keys, so 1000 found users
-- with tags of AWS's longest values must fit.
ALTER TABLE backup_storage_accounts DROP CONSTRAINT backup_storage_accounts_report_check;
ALTER TABLE backup_storage_accounts ADD CONSTRAINT backup_storage_accounts_report_check CHECK(octet_length(report::text) <= 4194304);

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
-- A report too large for the older limit is dropped, and its account checked again at once.
UPDATE backup_storage_accounts SET report = '{}', checked_at = NULL WHERE octet_length(report::text) > 262144;
ALTER TABLE backup_storage_accounts DROP CONSTRAINT backup_storage_accounts_report_check;
ALTER TABLE backup_storage_accounts ADD CONSTRAINT backup_storage_accounts_report_check CHECK(octet_length(report::text) <= 262144);
