-- +goose Up
-- A session's lineage is shared by every token that renews it, from the sign-in that started
-- it. A logout ends the whole lineage, so a renewal that replaced the logged-out token while
-- the logout ran cannot leave its successor signed in. Existing sessions each start their own.
-- astilba: not additive: the column has no default, so an older release's session insert fails.
ALTER TABLE sessions ADD COLUMN lineage uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE sessions ALTER COLUMN lineage DROP DEFAULT;
CREATE INDEX sessions_lineage_idx ON sessions (lineage);

-- +goose Down
DROP INDEX sessions_lineage_idx;
ALTER TABLE sessions DROP COLUMN lineage;
