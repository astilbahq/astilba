-- +goose Up
-- astilba: not additive: it numbers installation_key_events' existing rows in a backfill, and adds an identity column and a unique index to a table an older release writes.
-- The order installation key events were recorded in. Their time is the start of the transaction
-- that wrote them, so events can share one, and a database clock that steps back dates a later
-- event before an earlier one. Readers order by seq instead. The database assigns it, and a writer
-- cannot set or change it. Events from before it are numbered by time, then ID, the order readers
-- used until now.
ALTER TABLE installation_key_events ADD COLUMN seq bigint;
ALTER TABLE installation_key_events DISABLE TRIGGER installation_key_events_change;
UPDATE installation_key_events e SET seq = n.seq
FROM (SELECT id, row_number() OVER (ORDER BY at, id) AS seq FROM installation_key_events) n
WHERE e.id = n.id;
ALTER TABLE installation_key_events ENABLE TRIGGER installation_key_events_change;
ALTER TABLE installation_key_events ALTER COLUMN seq SET NOT NULL,
    ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY;
SELECT setval(pg_get_serial_sequence('installation_key_events', 'seq'), coalesce(max(seq), 0) + 1, false) FROM installation_key_events;
CREATE UNIQUE INDEX installation_key_events_seq ON installation_key_events (seq);

-- +goose Down
ALTER TABLE installation_key_events DROP COLUMN seq;
