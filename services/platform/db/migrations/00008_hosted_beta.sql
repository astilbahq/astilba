-- +goose Up
ALTER TABLE accounts ADD COLUMN email_verified_at timestamptz;
CREATE TABLE email_verifications (
 account_id uuid PRIMARY KEY REFERENCES accounts(id),
 token_hash bytea NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL
);
CREATE TABLE hosted_invitations (
 id uuid PRIMARY KEY,
 email text NOT NULL,
 token_hash bytea NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL,
 claimed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX hosted_invitation_open_email ON hosted_invitations(email) WHERE claimed_at IS NULL;
CREATE TABLE hosted_subscriptions (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),
 customer_id text UNIQUE,
 subscription_id text UNIQUE,
 status text NOT NULL DEFAULT 'pending',
 management_until timestamptz,
 server_limit integer NOT NULL CHECK(server_limit BETWEEN 1 AND 100),
 notified_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE hosted_checkouts (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),
 intent_id uuid NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL,
 session_id text UNIQUE,
 url text NOT NULL DEFAULT '',
 completed boolean NOT NULL DEFAULT false
);
CREATE TABLE hosted_billing_events (
 id text PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id),
 received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX execution_diagnostic_retention ON execution_operations(created_at)
 WHERE state IN ('succeeded','failed','cancelled') AND (steps<>'[]'::jsonb OR note<>'');
CREATE INDEX pipeline_log_retention ON pipeline_runs(created_at) WHERE result ? 'logs';

-- +goose Down
DROP INDEX pipeline_log_retention, execution_diagnostic_retention;
DROP TABLE hosted_billing_events, hosted_checkouts, hosted_subscriptions, hosted_invitations, email_verifications;
ALTER TABLE accounts DROP COLUMN email_verified_at;
