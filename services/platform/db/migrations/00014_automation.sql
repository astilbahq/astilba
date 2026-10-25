-- +goose Up
CREATE TABLE automation_credentials (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    creator_id uuid NOT NULL REFERENCES accounts(id),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    auth_version bigint NOT NULL,
    mfa_approved boolean NOT NULL,
    capabilities text[] NOT NULL CHECK (
        cardinality(capabilities) BETWEEN 1 AND 5 AND
        capabilities <@ ARRAY['environment:read','configuration:write','deployment:plan','deployment:apply','build:run']::text[]
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    revoked_at timestamptz,
    FOREIGN KEY (workspace_id, environment_id) REFERENCES environments(workspace_id, id)
);
CREATE INDEX automation_credentials_scope ON automation_credentials(workspace_id, environment_id, created_at DESC);
ALTER TABLE audit_events ADD COLUMN automation_credential_id uuid;
ALTER TABLE build_targets ADD COLUMN automatic_requested_commit text NOT NULL DEFAULT '';
UPDATE build_targets SET automatic_requested_commit=requested_commit;

-- +goose Down
ALTER TABLE build_targets DROP COLUMN automatic_requested_commit;
ALTER TABLE audit_events DROP COLUMN automation_credential_id;
DROP TABLE automation_credentials;
