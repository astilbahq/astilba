-- +goose Up
-- astilba: not additive: moves the installation ID out of dns_installation, which older releases read
-- The installation's identity. Its id is the installation ID: custody binds every key wrapping
-- to it, ASTILBA_INSTALLATION_ID pins it, and generated DNS records carry it as their owner. It
-- was made for DNS first, so it moves here and stays the same.
CREATE TABLE installation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO installation (id) SELECT id FROM dns_installation;
DROP TABLE dns_installation;

-- Every change to the installation's keys and custody, in the order it happened: a backend
-- registered, a canary made, a rewrap started and finished, a start refused. The operator's name
-- and reason stay here, never in a tenant's records, until the secret access retention period
-- passes, when they are blanked. Rows hold IDs and counts only, never key material, and are
-- otherwise never changed or removed.
CREATE TABLE installation_key_events (
    id uuid PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    -- One command's events share it: a start and its finish, and what happened between.
    run_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z-]{0,63}$'),
    operator text CHECK (length(operator) BETWEEN 1 AND 200),
    reason text CHECK (length(reason) BETWEEN 1 AND 1000),
    backend_id text CHECK (length(backend_id) BETWEEN 1 AND 64),
    -- The key an event is about, once there are workspace and installation keys.
    scope text CHECK (scope IN ('installation', 'workspace')),
    workspace_id uuid,
    generation bigint CHECK (generation >= 0),
    details jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object'),
    CHECK (workspace_id IS NULL OR scope = 'workspace')
);
CREATE INDEX installation_key_events_at ON installation_key_events (at);
CREATE INDEX installation_key_events_run ON installation_key_events (run_id);

-- The database's clock dates each event. Afterwards the only change allowed is blanking the
-- operator and reason of an event past the retention period, leaving every other column as it
-- was; the whole row is compared, so a column added later is kept too. This guards against
-- mistakes, not against whoever owns the database. The search path names pg_temp last, so a
-- caller's temporary table never stands in for the retention setting.
-- +goose StatementBegin
CREATE FUNCTION installation_key_events_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
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
CREATE TRIGGER installation_key_events_insert BEFORE INSERT ON installation_key_events FOR EACH ROW EXECUTE FUNCTION installation_key_events_guard();
CREATE TRIGGER installation_key_events_change BEFORE UPDATE OR DELETE ON installation_key_events FOR EACH ROW EXECUTE FUNCTION installation_key_events_guard();

-- Blanks the operator and reason of at most batch of the oldest events past the retention period.
-- It runs with its owner's rights, as expire_secret_access does, so the serving role needs no
-- right to change events itself.
-- +goose StatementBegin
CREATE FUNCTION blank_installation_key_operators(batch integer) RETURNS integer LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH blanked AS (
        UPDATE installation_key_events SET operator = NULL, reason = NULL WHERE id IN (
            SELECT id FROM installation_key_events
            WHERE (operator IS NOT NULL OR reason IS NOT NULL)
                AND at < now() - make_interval(days => (SELECT retention_days FROM secret_access_settings))
            ORDER BY at LIMIT least(greatest(batch, 1), 10000)
        ) RETURNING 1
    )
    SELECT count(*)::integer FROM blanked
$$;
-- +goose StatementEnd

-- The key services the installation's keys are wrapped under. Configuration names the backend an
-- instance reads from; this table says which configured backends new keys are wrapped for, so
-- the database can choose among backends but never add one an instance has not configured.
CREATE TABLE custody_backends (
    backend_id text PRIMARY KEY CHECK (length(backend_id) BETWEEN 1 AND 64),
    role text NOT NULL CHECK (role IN ('read+write', 'write', 'retiring')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One wrapped random key per backend and key class, bound to the installation ID. Start-up opens
-- each class it may decrypt with, so a different keyring, a different provider key or a database
-- copied under another installation ID refuses to start.
CREATE TABLE installation_key_canaries (
    backend_id text NOT NULL REFERENCES custody_backends (backend_id) ON DELETE CASCADE,
    class text NOT NULL CHECK (class ~ '^[a-z]{1,32}$'),
    wrapped bytea NOT NULL CHECK (length(wrapped) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (backend_id, class)
);

-- The last custody stage the installation completed. `astilba migrate` refuses a release more
-- than one stage ahead of it; the custody command that completes a stage advances it.
CREATE TABLE custody_stage (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    stage integer NOT NULL CHECK (stage >= 0),
    advanced_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO custody_stage (singleton, stage) VALUES (true, 1);

-- +goose Down
DROP TABLE custody_stage;
DROP TABLE installation_key_canaries;
DROP TABLE custody_backends;
DROP FUNCTION blank_installation_key_operators(integer);
DROP TABLE installation_key_events;
DROP FUNCTION installation_key_events_guard();
CREATE TABLE dns_installation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO dns_installation (singleton, id) SELECT true, id FROM installation;
DROP TABLE installation;
