-- +goose Up
-- astilba: not additive: an older release can't read the backup_exposure alert kind.
-- Raised for each backup destination whose storage keys a revoked server holds, until none does.
-- See docs/backups.md#destination-setup.
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held','backup_exposure'));


-- The latest discard of a restore's volume. While it is active the volume is neither cut over to
-- nor discarded again; a succeeded one deleted it, and a failed or cancelled one leaves it usable.
ALTER TABLE backup_recoveries ADD COLUMN discard_operation_id uuid,
 ADD FOREIGN KEY(workspace_id,discard_operation_id) REFERENCES execution_operations(workspace_id,id);

-- +goose Down
ALTER TABLE backup_recoveries DROP COLUMN discard_operation_id;
DELETE FROM operational_alerts WHERE kind = 'backup_exposure';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held'));
