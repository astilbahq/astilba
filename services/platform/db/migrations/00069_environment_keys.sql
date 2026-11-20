-- +goose Up
-- Each environment's own key, which its secret values are sealed under so that purging the
-- environment can destroy every copy of them at once: the key is a random 256-bit key held only
-- wrapped under its workspace's key, at the workspace key generation the row names. Destroying it
-- keeps the row, without the key, so recovery tells a destroyed key from a missing one.
--
-- Additive: the previous release neither reads nor writes it.
CREATE TABLE environment_keys (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation >= 1),
    state text NOT NULL CHECK (state IN ('active', 'destroyed')),
    -- The workspace key generation the key is wrapped under.
    workspace_generation bigint NOT NULL CHECK (workspace_generation >= 1),
    wrapped bytea CHECK (length(wrapped) BETWEEN 1 AND 4096),
    -- HMAC-SHA256 of a fixed label under the key, checked after every unwrap.
    kcv bytea CHECK (length(kcv) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    destroyed_at timestamptz,
    -- An environment's ID is unique within its workspace only, and importing keeps it, so a key
    -- is always named by both.
    PRIMARY KEY (workspace_id, environment_id, generation),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments (workspace_id, id) ON DELETE CASCADE,
    CHECK ((state = 'active') = (wrapped IS NOT NULL AND kcv IS NOT NULL AND destroyed_at IS NULL)),
    CHECK ((state = 'destroyed') = (wrapped IS NULL AND kcv IS NULL AND destroyed_at IS NOT NULL))
);
CREATE UNIQUE INDEX environment_keys_active ON environment_keys (workspace_id, environment_id) WHERE state = 'active';

-- A key starts active, above every generation its environment has had, and never for an
-- environment that is being purged or whose key was destroyed: a destroyed key is never replaced,
-- so nothing is sealed for the environment again. It changes only by being destroyed, which only
-- a purge, or the end of a preview, does. It is deleted only with its environment, and never
-- truncated. This guards against mistakes, not against whoever owns the database.
-- +goose StatementBegin
CREATE FUNCTION environment_key_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
    top bigint;
    env_state text;
    env_purpose text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT EXISTS (SELECT 1 FROM environments WHERE workspace_id = OLD.workspace_id AND id = OLD.environment_id) THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'environment keys are destroyed, never deleted, except with their environment';
    END IF;
    SELECT state, purpose INTO env_state, env_purpose FROM environments
    WHERE workspace_id = NEW.workspace_id AND id = NEW.environment_id FOR SHARE;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'active' THEN
            RAISE EXCEPTION 'a new environment key starts active';
        END IF;
        IF env_state IN ('purging', 'purged') OR EXISTS (SELECT 1 FROM environment_keys WHERE workspace_id = NEW.workspace_id AND environment_id = NEW.environment_id AND state = 'destroyed') THEN
            RAISE EXCEPTION 'an environment whose key was destroyed gets no other';
        END IF;
        SELECT max(generation) INTO top FROM environment_keys WHERE workspace_id = NEW.workspace_id AND environment_id = NEW.environment_id;
        IF NEW.generation <= coalesce(top, 0) THEN
            RAISE EXCEPTION 'a new environment key generation must be above every existing one';
        END IF;
        NEW.created_at := now();
        RETURN NEW;
    END IF;
    IF OLD.state <> 'active' OR NEW.state <> 'destroyed'
        OR to_jsonb(NEW) - 'state' - 'wrapped' - 'kcv' - 'destroyed_at' <> to_jsonb(OLD) - 'state' - 'wrapped' - 'kcv' - 'destroyed_at' THEN
        RAISE EXCEPTION 'an environment key changes only by being destroyed';
    END IF;
    IF NOT (env_state IN ('purging', 'purged') OR env_purpose = 'preview' AND NOT EXISTS (
        SELECT 1 FROM preview_requests r WHERE r.workspace_id = NEW.workspace_id AND r.environment_id = NEW.environment_id AND r.state <> 'deleted')) THEN
        RAISE EXCEPTION 'only purging an environment, or ending its preview, destroys its key';
    END IF;
    NEW.destroyed_at := now();
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER environment_keys_guard BEFORE INSERT OR UPDATE OR DELETE ON environment_keys FOR EACH ROW EXECUTE FUNCTION environment_key_guard();
CREATE TRIGGER environment_keys_truncate BEFORE TRUNCATE ON environment_keys FOR EACH STATEMENT EXECUTE FUNCTION custody_keys_truncate_guard();
INSERT INTO schema_additive_migrations (version) VALUES (69);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 69;
DROP TABLE environment_keys;
DROP FUNCTION environment_key_guard();
