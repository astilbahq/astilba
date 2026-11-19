-- +goose Up
-- A recovery base's window cut counts the runtime changes recorded or ended inside the window
-- (docs/cross-host-placement.md). Read through execution_operations_applied_history, which orders
-- each environment's changes by generation, it read every change the workspace ever made, on
-- every pass of every capture. These order them by time, so a capture reads only those near its
-- window, and its cost follows the window rather than the workspace's age:
--   * execution_operations_applied_times: runtime changes by the latest of when each was recorded
--     and when it ended, each standing in for by its creation while it has none. A change counts
--     towards the window from the later of those, less its margin, so this bounds the read.
--   * recovered_runtime_history_recorded: applied history a recovery kept without its rows, by when
--     each change was recorded.
-- Each environment's floor, the newest change it had when the window opened, is still found by
-- generation, through execution_operations_applied_history and recovered_runtime_history_chain.
-- `astilba migrate` runs only with management stopped (docs/management-upgrades.md), so the builds
-- block no writers, and plain builds inside the migration's transaction stay atomic and
-- re-runnable, as CONCURRENTLY would not.
CREATE INDEX execution_operations_applied_times ON execution_operations(workspace_id, (GREATEST(COALESCE(runtime_recorded_at, created_at), COALESCE(ended_at, created_at)))) WHERE runtime_outcome IS NOT NULL;
CREATE INDEX recovered_runtime_history_recorded ON recovered_runtime_history(workspace_id, recorded_at);
INSERT INTO schema_additive_migrations (version) VALUES (62);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 62;
DROP INDEX recovered_runtime_history_recorded;
DROP INDEX execution_operations_applied_times;
