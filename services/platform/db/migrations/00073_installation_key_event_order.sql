-- +goose Up
-- astilba: not additive: it numbers installation_key_events' existing rows in a backfill, adds a column its guard fills and a unique index to a table an older release writes, and replaces that guard.
-- The order installation key events were recorded in. Their time is the start of the transaction
-- that wrote them, so events can share one, and a database clock that steps back dates a later
-- event before an earlier one. Readers order by seq instead. Events from before it are numbered
-- by time, then ID, the order readers used until now.
ALTER TABLE installation_key_events ADD COLUMN seq bigint;
CREATE SEQUENCE installation_key_events_order OWNED BY installation_key_events.seq;
ALTER TABLE installation_key_events DISABLE TRIGGER installation_key_events_change;
UPDATE installation_key_events e SET seq = n.seq
FROM (SELECT id, row_number() OVER (ORDER BY at, id) AS seq FROM installation_key_events) n
WHERE e.id = n.id;
ALTER TABLE installation_key_events ENABLE TRIGGER installation_key_events_change;
ALTER TABLE installation_key_events ALTER COLUMN seq SET NOT NULL;
SELECT setval('installation_key_events_order', coalesce(max(seq), 0) + 1, false) FROM installation_key_events;
CREATE UNIQUE INDEX installation_key_events_seq ON installation_key_events (seq);

-- The database's clock dates each event and its sequence numbers it, replacing whatever a writer
-- gave for either. Afterwards the only change allowed is blanking the operator and reason of an
-- event past the retention period, leaving every other column as it was; the whole row is
-- compared, so a column added later is kept too. This guards against mistakes, not against
-- whoever owns the database. The search path names pg_temp last, so a caller's temporary table
-- never stands in for the retention setting or the sequence.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION installation_key_events_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.at := now();
        NEW.seq := nextval('installation_key_events_order');
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.operator IS NULL AND NEW.reason IS NULL
        AND to_jsonb(NEW) - 'operator' - 'reason' = to_jsonb(OLD) - 'operator' - 'reason'
        AND OLD.at < now() - make_interval(days => (SELECT retention_days FROM secret_access_settings)) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'installation key events are never changed or removed, except to blank an operator past retention';
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION installation_key_events_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.at := now();
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.operator IS NULL AND NEW.reason IS NULL
        AND to_jsonb(NEW) - 'operator' - 'reason' = to_jsonb(OLD) - 'operator' - 'reason'
        AND OLD.at < now() - make_interval(days => (SELECT retention_days FROM secret_access_settings)) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'installation key events are never changed or removed, except to blank an operator past retention';
END
$$;
-- +goose StatementEnd
ALTER TABLE installation_key_events DROP COLUMN seq;
