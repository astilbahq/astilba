-- +goose Up
-- Operation rows are permanent, so a lookup that the indexes can't narrow reads a workspace's whole
-- history, and its cost grows until PostgreSQL's JIT threshold makes every call compile. These
-- indexes narrow the lookups that remained after 44:
--   * execution_parent: a coordinated root's children, which every coordinator pass, maintenance
--     pass and dependent child's dispatch reads. Children are a few per root, so this holds only
--     operations that have a parent.
--   * execution_unfinished_workspace: unfinished operations in an environment or a workspace,
--     which admission, previews, pipelines and automatic deployments check. The states are listed,
--     not the ended ones excluded, because PostgreSQL estimates `NOT IN` as if most rows matched.
--   * execution_network_configure: a host's network configure commands by manifest generation,
--     which the network sweep checks before it sends a removal. A host has one configure command
--     for each generation it reaches, each retried only a few times.
--   * execution_releases: an environment's newest successful deployments with their plans, which
--     its release history and every export list, newest first and only so many.
-- `astilba migrate` runs only with management stopped (docs/management-upgrades.md), so the builds
-- block no writers, and plain builds inside the migration's transaction stay atomic and
-- re-runnable, as CONCURRENTLY would not.
CREATE INDEX execution_parent ON execution_operations(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX execution_unfinished_workspace ON execution_operations(workspace_id, environment_id) WHERE state IN ('queued','dispatched','running','needs_attention');
CREATE INDEX execution_network_configure ON execution_operations(server_id, (command->'network'->'manifest'->'generation')) WHERE kind = 'network' AND command->'network'->>'action' = 'configure';
CREATE INDEX execution_releases ON execution_operations(workspace_id, environment_id, created_at DESC, id DESC) WHERE state = 'succeeded' AND command->'visual'->>'action' = 'deploy' AND visual_plan_id IS NOT NULL;
INSERT INTO schema_additive_migrations (version) VALUES (52);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 52;
DROP INDEX execution_releases;
DROP INDEX execution_network_configure;
DROP INDEX execution_unfinished_workspace;
DROP INDEX execution_parent;
