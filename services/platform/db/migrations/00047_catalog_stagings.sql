-- +goose Up
-- One row each time a catalog entry is staged into an environment: which entry and release, the
-- draft revision staging saved, and the services it created, as an object from the entry's
-- placeholder service IDs to the new ones. The backups page recommends the entry's backup
-- presets from it. The environment may since have removed or renamed those services, so readers
-- match them against its current specification. Rows are never changed.
CREATE TABLE catalog_stagings (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    id uuid NOT NULL,
    recipe_id text NOT NULL CHECK (length(recipe_id) BETWEEN 1 AND 64),
    recipe_version text NOT NULL CHECK (length(recipe_version) <= 64),
    revision_id uuid NOT NULL,
    services jsonb NOT NULL CHECK (jsonb_typeof(services) = 'object'),
    author_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, environment_id, revision_id) REFERENCES specification_revisions(workspace_id, environment_id, id) ON DELETE CASCADE
);
CREATE INDEX catalog_stagings_environment ON catalog_stagings (workspace_id, environment_id, created_at);
INSERT INTO schema_additive_migrations (version) VALUES (47);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 47;
DROP TABLE catalog_stagings;
