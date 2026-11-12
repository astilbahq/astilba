-- +goose Up
-- An agent polls for its next command about once a second, and placement, upgrades, pipelines and
-- networks ask whether a host has unfinished work. Without this index each of those read every
-- operation in the table, so they grew with every workspace's history, and once the planner's
-- estimate passed PostgreSQL's JIT threshold, a poll spent hundreds of milliseconds compiling a
-- query that ran in two. A host's unfinished operations are few however long its history is.
CREATE INDEX execution_unfinished_server ON execution_operations(server_id, created_at) WHERE state NOT IN ('succeeded','failed','cancelled');
INSERT INTO schema_additive_migrations (version) VALUES (43);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 43;
DROP INDEX execution_unfinished_server;
