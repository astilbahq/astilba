-- +goose Up
-- astilba: not additive: an older release can't read the recovery_window alert kind, and it captures recovery bases that a later one would read without their window.
-- A recovery base carries a window of applied history instead of every operation that changed a
-- runtime (docs/cross-host-placement.md). These record what that needs.

-- The window each base this management captured covers, which the Workspace page and the
-- recovery_window alert show for the newest one. A base captured before this migration has none.
ALTER TABLE recovery_bases ADD COLUMN window_start timestamptz;
CREATE INDEX recovery_bases_newest ON recovery_bases(workspace_id,captured_at);

-- Applied-history entries a recovery took from a base without the operation row they describe.
-- A later base carries them in its ledger, so a second recovery from a backup taken inside the
-- first one's gap still finds the runtime that backup applied. entry is the sealed entry as the
-- base carried it.
CREATE TABLE recovered_runtime_history (
 workspace_id uuid NOT NULL REFERENCES workspaces(id),
 operation_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 generation bigint NOT NULL,
 recorded_at timestamptz NOT NULL,
 entry jsonb NOT NULL,
 PRIMARY KEY(workspace_id,operation_id)
);
CREATE INDEX recovered_runtime_history_chain ON recovered_runtime_history(workspace_id,environment_id,generation);

-- Raised while the newest base's window is shorter than 35 days, the backup retention the docs
-- suggest, so a backup old enough to need is no longer one recovery can use.
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window'));

-- +goose Down
DELETE FROM operational_alerts WHERE kind = 'recovery_window';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict'));
DROP TABLE recovered_runtime_history;
DROP INDEX recovery_bases_newest;
ALTER TABLE recovery_bases DROP COLUMN window_start;
