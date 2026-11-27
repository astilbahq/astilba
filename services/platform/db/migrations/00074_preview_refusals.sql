-- +goose Up
-- A preview step refused for good: creating its environment, admitting its build or promoting
-- the build. refused_key names the step and digests everything it read that a later pass could
-- find changed, and refusal_note is the reason the owner was shown. A pass whose step has the same
-- key skips it and shows the reason again, so the refused work is not repeated every five minutes.
-- '' for a request with no step refused, or one refused before this was recorded.
--
-- Additive: the previous release neither reads nor writes them, so while it still serves passes
-- it repeats a refused step as before. A key matches only the inputs it was refused for, so a
-- step that release lets through, or a refusal it records over, is skipped only when the same
-- inputs come back.
ALTER TABLE preview_requests ADD COLUMN refused_key text NOT NULL DEFAULT '';
ALTER TABLE preview_requests ADD COLUMN refusal_note text NOT NULL DEFAULT '';

INSERT INTO schema_additive_migrations (version) VALUES (74);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 74;
ALTER TABLE preview_requests DROP COLUMN refusal_note;
ALTER TABLE preview_requests DROP COLUMN refused_key;
