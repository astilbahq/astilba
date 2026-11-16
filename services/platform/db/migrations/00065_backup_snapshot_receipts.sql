-- +goose Up
-- When a server was revoked, and when management first learned of each backup snapshot
-- (docs/backups.md). A server reports its own times for the snapshots it uploads, and a host
-- that was compromised before it was revoked can backdate them. A snapshot's ID is the SHA-256 of
-- its content, so the time management first received an ID, by its own clock, bounds when that
-- content existed whatever the host claims. Restoring onto a replacement server shows each
-- snapshot by this time against the revocation.

-- When the revocation in force began, or NULL when the server isn't revoked or was revoked before
-- this was recorded. Only read while revoked is set: a release without this column leaves it
-- behind when it renews a server, and a stale time is always earlier than the revocation it
-- stands for, which only widens what is shown as uploaded after it.
ALTER TABLE servers ADD COLUMN revoked_at timestamptz;

-- Each snapshot the policy's reports list as available, by ID, with management's clock when an
-- upload first reported it: {"<snapshot>": "<time>"}. Only the policy's server reports for it. A
-- host can take a snapshot out of its reports, which removes it, and report it again later, which
-- records a later time: it can never make a snapshot look older than management saw it. Only
-- recovery moves a time, to an earlier one another copy of the workspace recorded. One value per
-- policy, like its status, keeps an archive to one record per policy however many snapshots it
-- retains.
ALTER TABLE backup_policies ADD COLUMN snapshot_receipts jsonb NOT NULL DEFAULT '{}';
INSERT INTO schema_additive_migrations (version) VALUES (65);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 65;
ALTER TABLE backup_policies DROP COLUMN snapshot_receipts;
ALTER TABLE servers DROP COLUMN revoked_at;
