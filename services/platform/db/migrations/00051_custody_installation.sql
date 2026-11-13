-- +goose Up
-- astilba: not additive: moves the installation ID out of dns_installation, which older releases read
-- The installation's identity. Its id is the installation ID: custody binds every key wrapping
-- to it, ASTILBA_INSTALLATION_ID pins it, and generated DNS records carry it as their owner. It
-- was made for DNS first, so it moves here and stays the same.
CREATE TABLE installation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO installation (id) SELECT id FROM dns_installation;
DROP TABLE dns_installation;

-- Every change to the installation's keys and custody, in the order it happened: a backend
-- registered, a canary made, a rewrap started and finished, a start refused. The operator's name
-- and reason stay here, never in a tenant's records. Rows hold IDs and counts only, never key
-- material, and are never changed or removed.
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

-- The database's clock dates each event, and no event is changed or removed afterwards.
-- +goose StatementBegin
CREATE FUNCTION installation_key_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.at := now();
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'installation key events are never changed or removed';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER installation_key_events_insert BEFORE INSERT ON installation_key_events FOR EACH ROW EXECUTE FUNCTION installation_key_events_guard();
CREATE TRIGGER installation_key_events_change BEFORE UPDATE OR DELETE ON installation_key_events FOR EACH ROW EXECUTE FUNCTION installation_key_events_guard();

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
DROP TABLE installation_key_events;
DROP FUNCTION installation_key_events_guard();
CREATE TABLE dns_installation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO dns_installation (singleton, id) SELECT true, id FROM installation;
DROP TABLE installation;
