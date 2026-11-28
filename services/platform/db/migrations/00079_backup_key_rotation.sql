-- +goose Up
-- astilba: not additive: it adds a column with a CHECK, which the additive check can't verify. An older release never writes it, and the default passes.
-- Rotating a managed destination's key without revoking its server. See docs/backups.md#managed-keys.
-- key_rotation_days is how old a storage account's destinations' current keys may grow before
-- management replaces them, or NULL for never. It starts at 90 for every account, existing ones
-- included, as the CIS AWS Foundations Benchmark asks of access keys.
ALTER TABLE backup_storage_accounts ADD COLUMN key_rotation_days integer DEFAULT 90 CHECK(key_rotation_days BETWEEN 30 AND 365);
-- When the owner last asked for a destination's key to be replaced. The request stands until a
-- key activates after it, so a mint that fails is tried again.
ALTER TABLE backup_destinations ADD COLUMN rekey_requested_at timestamptz;

-- +goose Down
ALTER TABLE backup_destinations DROP COLUMN rekey_requested_at;
ALTER TABLE backup_storage_accounts DROP COLUMN key_rotation_days;
