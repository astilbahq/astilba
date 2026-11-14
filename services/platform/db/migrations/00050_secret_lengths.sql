-- +goose Up
-- How long each secret version's value is, plain and as a JSON string, so that planning can
-- check a plan's secrets fit without opening them. Versions written before this, or by the
-- previous release while rolled back to it, have none until a job fills them in.
ALTER TABLE secret_versions ADD COLUMN plaintext_bytes integer;
ALTER TABLE secret_versions ADD COLUMN json_bytes integer;
INSERT INTO schema_additive_migrations (version) VALUES (50);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 50;
ALTER TABLE secret_versions DROP COLUMN json_bytes;
ALTER TABLE secret_versions DROP COLUMN plaintext_bytes;
