-- +goose Up
-- Trusted App grants are controller-local. Workspace archives contain only the
-- public authentication marker and must be authorized again on the destination.
CREATE TABLE github_app_authorizations (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 auth_version bigint NOT NULL,
 session_hash bytea NOT NULL,
 state_hash bytea NOT NULL UNIQUE,
 app_id bigint NOT NULL,
 encrypted_access bytea NOT NULL,
 github_user_id bigint,
 github_login text NOT NULL DEFAULT '',
 status text NOT NULL CHECK(status IN ('pending','ready','consumed','failed')),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX github_app_authorizations_workspace ON github_app_authorizations(workspace_id,created_at);
CREATE TABLE github_app_connections (
 workspace_id uuid NOT NULL,
 connection_id uuid PRIMARY KEY,
 app_id bigint NOT NULL,
 installation_id bigint NOT NULL,
 repository_id bigint NOT NULL,
 github_user_id bigint NOT NULL,
 actor_id uuid NOT NULL REFERENCES accounts(id),
 generation bigint NOT NULL DEFAULT 1,
 active boolean NOT NULL DEFAULT true,
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX github_app_connections_installation ON github_app_connections(app_id,installation_id,connection_id);
CREATE TABLE github_app_deliveries (
 delivery_id text PRIMARY KEY,
 body_hash text NOT NULL,
 event jsonb NOT NULL,
 cursor uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 completed boolean NOT NULL DEFAULT false,
 received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX github_app_deliveries_retention ON github_app_deliveries(completed,received_at);
