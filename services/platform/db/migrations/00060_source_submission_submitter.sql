-- +goose Up
-- A source submission is sent on its submitter's say-so, and can wait up to an hour before it is.
-- It records the submitter's authentication version when they submitted, and is sent only while
-- they are still an owner or operator of the workspace with that version, the check every
-- standing approval makes. A NULL version is a submission an older release queued, which is
-- refused with a note to submit it again. Losing write access fails a queued submission, which an
-- older release obeys too; withdrawn_at marks that, so an attempt already sending it can record what
-- the provider did, and no other attempt for the revision starts beside it.
ALTER TABLE source_submissions ADD COLUMN actor_auth_version bigint;
ALTER TABLE source_submissions ADD COLUMN withdrawn_at timestamptz;
INSERT INTO schema_additive_migrations (version) VALUES (60);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 60;
ALTER TABLE source_submissions DROP COLUMN withdrawn_at;
ALTER TABLE source_submissions DROP COLUMN actor_auth_version;
