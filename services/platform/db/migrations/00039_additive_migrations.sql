-- +goose Up
-- A migration that only adds to the schema records its version here, in its own transaction.
-- Adding means new tables, nullable columns (or ones with a default), new indexes, and
-- functions or triggers on its own new tables: a release before it runs unchanged on the
-- result. A binary starts on a database ahead of it only when every newer migration recorded
-- itself here, so rolling back to the previous release is then a redeploy of its image, with no
-- restore. Migrations that do not add only never record themselves, and `db/migrate_test.go`
-- holds each file to what it claims.
CREATE TABLE schema_additive_migrations (
    version bigint PRIMARY KEY CHECK (version > 0),
    recorded_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_additive_migrations (version) VALUES (39);

-- +goose Down
DROP TABLE schema_additive_migrations;
