-- +goose Up
-- One row for every time management is given the keys to a workspace's stored values, or to
-- the installation's. It is written, in its own transaction, before anything is opened, so the
-- record over-reports and never under-reports. Rows hold IDs only, never names or values, and
-- are never changed. There is no reference to the workspace: a record outlives the rows it
-- names until it expires.
CREATE TABLE secret_access (
    id uuid PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    workspace_id uuid,
    purpose text NOT NULL CHECK (purpose IN ('seal','deliver','operate','inspect','copy','export','recover','rotate','authenticate','token')),
    principal_kind text NOT NULL CHECK (principal_kind IN ('account','automation','agent','system','operator')),
    account_id uuid,
    automation_id uuid,
    server_id uuid,
    agent_instance text CHECK (length(agent_instance) <= 200),
    job text CHECK (length(job) <= 64),
    operator_ref text CHECK (length(operator_ref) <= 200),
    operation_id uuid,
    items jsonb NOT NULL CHECK (jsonb_typeof(items) = 'array'),
    item_count integer NOT NULL CHECK (item_count >= 0),
    generations jsonb NOT NULL CHECK (jsonb_typeof(generations) = 'array'),
    backend_id text NOT NULL CHECK (length(backend_id) BETWEEN 1 AND 64),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 64)
);
CREATE INDEX secret_access_workspace_at ON secret_access (workspace_id, at);
CREATE INDEX secret_access_at ON secret_access (at);

-- How each record ended. Several rows for one record are possible: a session's close and the
-- sweep that finds it abandoned can both write one.
CREATE TABLE secret_access_outcomes (
    access_id uuid NOT NULL REFERENCES secret_access (id) ON DELETE CASCADE,
    at timestamptz NOT NULL DEFAULT now(),
    outcome text NOT NULL CHECK (outcome IN ('released','used','refused','failed','abandoned')),
    provider_request_ids jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(provider_request_ids) = 'array')
);
CREATE INDEX secret_access_outcomes_access ON secret_access_outcomes (access_id);

-- The database's clock, not the writer's, dates each row.
-- +goose StatementBegin
CREATE FUNCTION secret_access_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.at := now();
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER secret_access_at BEFORE INSERT ON secret_access FOR EACH ROW EXECUTE FUNCTION secret_access_at();
CREATE TRIGGER secret_access_outcomes_at BEFORE INSERT ON secret_access_outcomes FOR EACH ROW EXECUTE FUNCTION secret_access_at();
INSERT INTO schema_additive_migrations (version) VALUES (43);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 43;
DROP TABLE secret_access_outcomes;
DROP TABLE secret_access;
DROP FUNCTION secret_access_at();
