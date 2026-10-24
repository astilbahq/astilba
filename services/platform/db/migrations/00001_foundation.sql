-- +goose Up
CREATE TABLE accounts (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    password_hash text NOT NULL,
    auth_version bigint NOT NULL DEFAULT 1,
    totp_secret bytea,
    totp_pending bytea,
    totp_pending_expiry timestamptz,
    totp_last_step bigint NOT NULL DEFAULT -1,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (token text PRIMARY KEY, data bytea NOT NULL, expiry timestamptz NOT NULL);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);
CREATE TABLE workspaces (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    require_mfa boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE memberships (
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    account_id uuid NOT NULL REFERENCES accounts(id),
    role text NOT NULL CHECK (role IN ('owner', 'operator', 'viewer')),
    PRIMARY KEY (workspace_id, account_id)
);
CREATE TABLE invitations (
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    id uuid NOT NULL,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('owner', 'operator', 'viewer')),
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, email)
);
CREATE TABLE password_resets (
    account_id uuid PRIMARY KEY REFERENCES accounts(id),
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL
);
CREATE TABLE recovery_codes (
    account_id uuid NOT NULL REFERENCES accounts(id),
    code_hash bytea NOT NULL,
    PRIMARY KEY (account_id, code_hash)
);
CREATE TABLE auth_attempts (
    scope_hash bytea PRIMARY KEY,
    attempts integer NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_attempts_expiry_idx ON auth_attempts(expires_at);
CREATE TABLE projects (
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, name)
);
CREATE TABLE environments (
    workspace_id uuid NOT NULL,
    project_id uuid NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    configuration_owner text NOT NULL DEFAULT 'dashboard' CHECK (configuration_owner = 'dashboard'),
    draft_revision_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, project_id) REFERENCES projects(workspace_id, id),
    UNIQUE (workspace_id, project_id, name)
);
CREATE TABLE deployment_units (
    workspace_id uuid NOT NULL,
    id uuid NOT NULL,
    environment_id uuid NOT NULL,
    runtime text NOT NULL DEFAULT 'docker' CHECK (runtime = 'docker'),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, environment_id),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
CREATE TABLE specification_revisions (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    id uuid NOT NULL,
    author_id uuid NOT NULL REFERENCES accounts(id),
    specification jsonb NOT NULL CHECK (jsonb_typeof(specification) = 'object' AND octet_length(specification::text) <= 131072),
    content_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, environment_id, id),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
ALTER TABLE environments ADD CONSTRAINT environment_draft_owner
    FOREIGN KEY (workspace_id, id, draft_revision_id)
    REFERENCES specification_revisions(workspace_id, environment_id, id)
    DEFERRABLE INITIALLY DEFERRED;
-- +goose StatementBegin
CREATE FUNCTION immutable_specification() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'specification revisions are immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER specification_immutable BEFORE UPDATE OR DELETE ON specification_revisions
    FOR EACH ROW EXECUTE FUNCTION immutable_specification();
CREATE TABLE environment_layouts (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    positions jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(positions) = 'object' AND octet_length(positions::text) <= 32768),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, environment_id),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
CREATE TABLE secrets (
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id),
    UNIQUE (workspace_id, environment_id, name)
);
CREATE TABLE secret_versions (
    workspace_id uuid NOT NULL,
    secret_id uuid NOT NULL,
    id uuid NOT NULL,
    encrypted_value bytea NOT NULL,
    author_id uuid NOT NULL REFERENCES accounts(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id, secret_id) REFERENCES secrets(workspace_id, id)
);
CREATE TABLE audit_events (
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    id uuid NOT NULL,
    actor_id uuid NOT NULL REFERENCES accounts(id),
    action text NOT NULL,
    resource_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX audit_events_recent_idx ON audit_events(workspace_id, created_at DESC);

-- +goose Down
DROP TABLE audit_events, secret_versions, secrets, environment_layouts;
ALTER TABLE environments DROP CONSTRAINT environment_draft_owner;
DROP TABLE specification_revisions;
DROP FUNCTION immutable_specification();
DROP TABLE deployment_units, environments, projects, auth_attempts, recovery_codes, password_resets, invitations, memberships, workspaces, sessions, accounts;
