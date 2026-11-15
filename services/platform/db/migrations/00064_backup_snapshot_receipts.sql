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

-- One row for each snapshot its policy's reports list as available. first_reported_at is
-- management's clock when the snapshot was first reported, and server_id the server whose
-- authenticated report it came in. A host can take a snapshot out of its reports, which removes the
-- row, and report it again later, which records a later time: it can never make a snapshot look
-- older than management saw it. Only recovery moves a row, to an earlier time another copy of the
-- workspace recorded for it.
CREATE TABLE backup_snapshot_receipts (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    snapshot text NOT NULL CHECK (snapshot ~ '^[a-f0-9]{64}$'),
    server_id uuid NOT NULL,
    first_reported_at timestamptz NOT NULL,
    PRIMARY KEY (policy_id, snapshot),
    FOREIGN KEY (workspace_id, environment_id, policy_id) REFERENCES backup_policies(workspace_id, environment_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, server_id) REFERENCES servers(workspace_id, id) ON DELETE CASCADE
);

-- +goose StatementBegin
CREATE FUNCTION backup_snapshot_receipt_earlier() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.workspace_id, NEW.environment_id, NEW.policy_id, NEW.snapshot) IS DISTINCT FROM (OLD.workspace_id, OLD.environment_id, OLD.policy_id, OLD.snapshot)
        OR NEW.first_reported_at > OLD.first_reported_at
        OR NEW.first_reported_at = OLD.first_reported_at AND NEW.server_id IS DISTINCT FROM OLD.server_id THEN
        RAISE EXCEPTION 'a snapshot receipt only moves to an earlier time';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER backup_snapshot_receipt_earlier BEFORE UPDATE ON backup_snapshot_receipts
    FOR EACH ROW EXECUTE FUNCTION backup_snapshot_receipt_earlier();
INSERT INTO schema_additive_migrations (version) VALUES (64);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 64;
DROP TABLE backup_snapshot_receipts;
DROP FUNCTION backup_snapshot_receipt_earlier();
ALTER TABLE servers DROP COLUMN revoked_at;
