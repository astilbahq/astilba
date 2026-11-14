-- +goose Up
-- Each workspace's own key, and the installation's keys for second factors and self-issued
-- tokens. A key is a random 256-bit key held only wrapped: once per purpose and per backend that
-- new keys are wrapped for, under the backend's key for the purpose's class. A value's data key
-- is wrapped under one generation of its scope's key. An older release neither reads nor writes
-- these tables, and deleting a workspace deletes its keys, which is what makes deletion final.
CREATE TABLE workspace_keys (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    generation bigint NOT NULL CHECK (generation >= 1),
    -- active seals new values; retiring is being moved off; retained is opened only to recover
    -- data that may still use it; retired has no wrappings left.
    state text NOT NULL CHECK (state IN ('active', 'retiring', 'retained', 'retired')),
    -- A generation that may be compromised is opened but never sealed under again.
    sealable boolean NOT NULL DEFAULT true,
    -- HMAC-SHA256 of a fixed label under the key, checked after every unwrap.
    kcv bytea NOT NULL CHECK (length(kcv) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    PRIMARY KEY (workspace_id, generation)
);
CREATE UNIQUE INDEX workspace_keys_active ON workspace_keys (workspace_id) WHERE state = 'active';

-- The provider key a wrapping is made under is never stored: configuration names each class's.
CREATE TABLE workspace_key_wrappings (
    workspace_id uuid NOT NULL,
    generation bigint NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('seal', 'deliver', 'operate', 'inspect', 'copy', 'export', 'recover', 'rotate')),
    backend_id text NOT NULL REFERENCES custody_backends (backend_id) ON DELETE CASCADE,
    wrapped bytea NOT NULL CHECK (length(wrapped) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, generation, purpose, backend_id),
    FOREIGN KEY (workspace_id, generation) REFERENCES workspace_keys (workspace_id, generation) ON DELETE CASCADE
);

-- The installation key seals second factors; token keys seal the tokens management issues to
-- itself, and every instance holds each generation not retired.
CREATE TABLE installation_keys (
    scope text NOT NULL CHECK (scope IN ('installation', 'token')),
    generation bigint NOT NULL CHECK (generation >= 1),
    state text NOT NULL CHECK (state IN ('active', 'retiring', 'retained', 'retired')),
    sealable boolean NOT NULL DEFAULT true,
    kcv bytea NOT NULL CHECK (length(kcv) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    PRIMARY KEY (scope, generation)
);
CREATE UNIQUE INDEX installation_keys_active ON installation_keys (scope) WHERE state = 'active';

CREATE TABLE installation_key_wrappings (
    scope text NOT NULL,
    generation bigint NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('seal', 'authenticate', 'token', 'rotate')),
    backend_id text NOT NULL REFERENCES custody_backends (backend_id) ON DELETE CASCADE,
    wrapped bytea NOT NULL CHECK (length(wrapped) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, generation, purpose, backend_id),
    FOREIGN KEY (scope, generation) REFERENCES installation_keys (scope, generation) ON DELETE CASCADE,
    CHECK (scope = 'installation' AND purpose IN ('seal', 'authenticate', 'rotate') OR scope = 'token' AND purpose IN ('token', 'rotate'))
);

-- Each running instance reports the token key generations it holds, so issuing moves to a new
-- generation only once every instance can open what it issues.
CREATE TABLE custody_instances (
    instance_id uuid PRIMARY KEY,
    token_generations bigint[] NOT NULL DEFAULT '{}',
    started_at timestamptz NOT NULL DEFAULT now(),
    seen_at timestamptz NOT NULL DEFAULT now()
);

-- Bumped whenever the set of backends new keys are wrapped for changes. A transaction storing a
-- new key holds it FOR SHARE and checks it is the version the key was wrapped for.
CREATE TABLE custody_backends_version (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version bigint NOT NULL CHECK (version >= 1)
);
INSERT INTO custody_backends_version (singleton, version) VALUES (true, 1);

-- +goose StatementBegin
CREATE FUNCTION custody_key_rank(state text) RETURNS integer LANGUAGE sql IMMUTABLE SET search_path = public, pg_temp AS $$
    SELECT array_position(ARRAY['active', 'retiring', 'retained', 'retired'], state)
$$;
-- +goose StatementEnd

-- A key generation starts active and sealable, above every generation its holder has had. Its
-- state only moves forward, sealable never comes back, and nothing else about it changes. A
-- workspace's keys are deleted only with the workspace, and the installation's never, so no
-- generation is ever reused. This guards against mistakes, not against whoever owns the database.
-- +goose StatementBegin
CREATE FUNCTION custody_key_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
    top bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'active' OR NOT NEW.sealable OR NEW.retired_at IS NOT NULL THEN
            RAISE EXCEPTION 'a new key generation starts active and sealable';
        END IF;
        IF TG_TABLE_NAME = 'workspace_keys' THEN
            SELECT max(generation) INTO top FROM workspace_keys WHERE workspace_id = NEW.workspace_id;
        ELSE
            SELECT max(generation) INTO top FROM installation_keys WHERE scope = NEW.scope;
        END IF;
        IF NEW.generation <= coalesce(top, 0) THEN
            RAISE EXCEPTION 'a new key generation must be above every existing one';
        END IF;
        NEW.created_at := now();
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF TG_TABLE_NAME = 'workspace_keys' AND NOT EXISTS (SELECT 1 FROM workspaces WHERE id = OLD.workspace_id) THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'key generations are retired, never deleted, except with their workspace';
    END IF;
    IF to_jsonb(NEW) - 'state' - 'sealable' - 'retired_at' <> to_jsonb(OLD) - 'state' - 'sealable' - 'retired_at'
        OR custody_key_rank(NEW.state) < custody_key_rank(OLD.state)
        OR NEW.sealable AND NOT OLD.sealable THEN
        RAISE EXCEPTION 'a key generation only moves forward: its state, and from sealable to not';
    END IF;
    NEW.retired_at := CASE WHEN NEW.state = 'retired' THEN coalesce(OLD.retired_at, now()) END;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER workspace_keys_guard BEFORE INSERT OR UPDATE OR DELETE ON workspace_keys FOR EACH ROW EXECUTE FUNCTION custody_key_guard();
CREATE TRIGGER installation_keys_guard BEFORE INSERT OR UPDATE OR DELETE ON installation_keys FOR EACH ROW EXECUTE FUNCTION custody_key_guard();

-- Sealing under a generation needs its seal wrapping, so the wrapping goes as soon as the
-- generation stops being active and sealable: what decides whether it may seal is then whether
-- anyone can unwrap the key for it, not a flag. A retired generation loses every wrapping.
-- +goose StatementBegin
CREATE FUNCTION custody_key_prune() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
BEGIN
    IF TG_TABLE_NAME = 'workspace_keys' THEN
        DELETE FROM workspace_key_wrappings
        WHERE workspace_id = NEW.workspace_id AND generation = NEW.generation
            AND (NEW.state = 'retired' OR purpose = 'seal' AND NOT (NEW.state = 'active' AND NEW.sealable));
    ELSE
        DELETE FROM installation_key_wrappings
        WHERE scope = NEW.scope AND generation = NEW.generation
            AND (NEW.state = 'retired' OR purpose = 'seal' AND NOT (NEW.state = 'active' AND NEW.sealable));
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER workspace_keys_prune AFTER UPDATE ON workspace_keys FOR EACH ROW EXECUTE FUNCTION custody_key_prune();
CREATE TRIGGER installation_keys_prune AFTER UPDATE ON installation_keys FOR EACH ROW EXECUTE FUNCTION custody_key_prune();

-- A seal wrapping is made only for an active, sealable generation, which is read FOR SHARE so it
-- cannot stop being one before this commits. A wrapping changes only by being wrapped again, and
-- goes only with its key, with its backend, once its generation is retired, or as a seal
-- wrapping its generation can no longer use.
-- +goose StatementBegin
CREATE FUNCTION custody_wrapping_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
    key_state text;
    key_sealable boolean;
    backend_role text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF to_jsonb(NEW) - 'wrapped' <> to_jsonb(OLD) - 'wrapped' THEN
            RAISE EXCEPTION 'a key wrapping changes only by being wrapped again';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF TG_TABLE_NAME = 'workspace_key_wrappings' THEN
            SELECT state, sealable INTO key_state, key_sealable FROM workspace_keys
            WHERE workspace_id = NEW.workspace_id AND generation = NEW.generation FOR SHARE;
        ELSE
            SELECT state, sealable INTO key_state, key_sealable FROM installation_keys
            WHERE scope = NEW.scope AND generation = NEW.generation FOR SHARE;
        END IF;
        IF key_state IS NULL OR key_state = 'retired' THEN
            RAISE EXCEPTION 'a key wrapping needs a generation that is not retired';
        END IF;
        IF NEW.purpose = 'seal' AND NOT (key_state = 'active' AND key_sealable) THEN
            RAISE EXCEPTION 'only an active, sealable generation is wrapped to seal';
        END IF;
        NEW.created_at := now();
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'workspace_key_wrappings' THEN
        SELECT state, sealable INTO key_state, key_sealable FROM workspace_keys
        WHERE workspace_id = OLD.workspace_id AND generation = OLD.generation;
    ELSE
        SELECT state, sealable INTO key_state, key_sealable FROM installation_keys
        WHERE scope = OLD.scope AND generation = OLD.generation;
    END IF;
    SELECT b.role INTO backend_role FROM custody_backends b WHERE b.backend_id = OLD.backend_id;
    IF key_state IS NULL OR key_state = 'retired' OR backend_role IS NULL OR backend_role = 'retiring'
        OR OLD.purpose = 'seal' AND NOT (key_state = 'active' AND key_sealable) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'a key wrapping goes only with its key, its backend, or its generation''s retirement';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER workspace_key_wrappings_guard BEFORE INSERT OR UPDATE OR DELETE ON workspace_key_wrappings FOR EACH ROW EXECUTE FUNCTION custody_wrapping_guard();
CREATE TRIGGER installation_key_wrappings_guard BEFORE INSERT OR UPDATE OR DELETE ON installation_key_wrappings FOR EACH ROW EXECUTE FUNCTION custody_wrapping_guard();

INSERT INTO schema_additive_migrations (version) VALUES (55);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 55;
DROP TABLE custody_backends_version;
DROP TABLE custody_instances;
DROP TABLE installation_key_wrappings;
DROP TABLE installation_keys;
DROP TABLE workspace_key_wrappings;
DROP TABLE workspace_keys;
DROP FUNCTION custody_wrapping_guard();
DROP FUNCTION custody_key_prune();
DROP FUNCTION custody_key_guard();
DROP FUNCTION custody_key_rank(text);
