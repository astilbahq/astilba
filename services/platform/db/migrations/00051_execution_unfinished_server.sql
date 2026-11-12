-- +goose Up
-- An agent polls for its next command about once a second, and placement, upgrades, pipelines,
-- networks and revocation ask whether a host has unfinished work. Without this index each of those
-- read every operation in the table, so they grew with every workspace's history, and once the
-- planner's estimate passed PostgreSQL's JIT threshold, a poll spent hundreds of milliseconds
-- compiling a query that ran in two. A host's unfinished operations are few however long its
-- history is. The states are listed, not the ended ones excluded: PostgreSQL estimates `NOT IN`
-- as if most rows matched, which would make it read history anyway. `astilba migrate` runs only
-- with management stopped (docs/management-upgrades.md), so the build blocks no writers, and a
-- plain build inside the migration's transaction stays atomic and re-runnable, as CONCURRENTLY
-- would not.
CREATE INDEX execution_unfinished_server ON execution_operations(server_id, created_at) WHERE state IN ('queued','dispatched','running','needs_attention');
INSERT INTO schema_additive_migrations (version) VALUES (51);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 51;
DROP INDEX execution_unfinished_server;
