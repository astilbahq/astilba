-- +goose Up
-- How long secret access records are kept. Only `astilba migrate` writes it, from
-- ASTILBA_SECRET_ACCESS_RETENTION_DAYS, so on hosted, where serving and migrating become two
-- roles, the serving role can read it but never shorten it.
CREATE TABLE secret_access_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    retention_days integer NOT NULL DEFAULT 400 CHECK (retention_days BETWEEN 30 AND 3650)
);
INSERT INTO secret_access_settings (singleton) VALUES (true);

-- Expiry removes at most batch of the oldest records past the retention period, and their
-- outcomes with them. It runs with its owner's rights, so the serving role needs no right to
-- delete records itself. Its search path names pg_temp last, so a caller's temporary table of
-- the same name never stands in for one of its tables.
-- +goose StatementBegin
CREATE FUNCTION expire_secret_access(batch integer) RETURNS integer LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH expired AS (
        DELETE FROM secret_access WHERE id IN (
            SELECT id FROM secret_access
            WHERE at < now() - make_interval(days => (SELECT retention_days FROM secret_access_settings))
            ORDER BY at LIMIT least(greatest(batch, 1), 10000)
        ) RETURNING 1
    )
    SELECT count(*)::integer FROM expired
$$;
-- +goose StatementEnd
INSERT INTO schema_additive_migrations (version) VALUES (49);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 49;
DROP FUNCTION expire_secret_access(integer);
DROP TABLE secret_access_settings;
