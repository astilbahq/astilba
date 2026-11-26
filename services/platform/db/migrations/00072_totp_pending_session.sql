-- +goose Up
-- The browser session lineage that began the account's pending authenticator enrolment. Only a
-- session of that lineage confirms the enrolment, and no other session replaces it while it lasts
-- and that lineage is signed in. NULL for an enrolment begun before this was recorded, which no
-- session confirms and any may replace.
--
-- Additive: the previous release neither reads nor writes it. An enrolment it begins keeps the
-- lineage an earlier one recorded, which can confirm it only with a code from the secret the
-- other session was shown.
ALTER TABLE accounts ADD COLUMN totp_pending_lineage uuid;

INSERT INTO schema_additive_migrations (version) VALUES (72);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 72;
ALTER TABLE accounts DROP COLUMN totp_pending_lineage;
