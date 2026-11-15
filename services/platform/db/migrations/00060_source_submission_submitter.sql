-- +goose Up
-- A source submission is sent on its submitter's say-so, and can wait up to an hour before it is.
-- It records the submitter's authentication version when they submitted, and is sent only while
-- they are still an owner or operator of the workspace with that version, the check every
-- standing approval makes. A NULL version is a submission an older release queued, which is
-- refused with a note to submit it again.
ALTER TABLE source_submissions ADD COLUMN actor_auth_version bigint;
INSERT INTO schema_additive_migrations (version) VALUES (60);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 60;
ALTER TABLE source_submissions DROP COLUMN actor_auth_version;
