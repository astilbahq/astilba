-- +goose Up
CREATE TABLE troubleshooting_policies (
 workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 enabled boolean NOT NULL,
 owners_only boolean NOT NULL,
 generation bigint NOT NULL CHECK(generation > 0),
 updated_by uuid NOT NULL REFERENCES accounts(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,environment_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);

CREATE TABLE troubleshooting_sessions (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 unit_id uuid NOT NULL,
 server_id uuid NOT NULL,
 application_id uuid NOT NULL,
 instance integer NOT NULL CHECK(instance BETWEEN 1 AND 40),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 auth_version bigint NOT NULL,
 browser_session_hash bytea,
 controller_generation bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('offered','active','closed')),
 ticket_hash bytea,
 ticket_expires_at timestamptz NOT NULL,
 deadline_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 closed_at timestamptz,
 close_reason text,
 input_bytes bigint NOT NULL DEFAULT 0,
 output_bytes bigint NOT NULL DEFAULT 0,
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,unit_id) REFERENCES deployment_units(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 CHECK((state = 'closed') = (closed_at IS NOT NULL)),
 CHECK((state = 'closed') = (close_reason IS NOT NULL)),
 CHECK(state = 'closed' OR browser_session_hash IS NOT NULL)
);
CREATE UNIQUE INDEX troubleshooting_one_per_target ON troubleshooting_sessions(server_id,environment_id,application_id,instance) WHERE state <> 'closed';
CREATE INDEX troubleshooting_open ON troubleshooting_sessions(workspace_id,environment_id) WHERE state <> 'closed';
CREATE INDEX troubleshooting_history ON troubleshooting_sessions(workspace_id,created_at DESC,id);

-- +goose Down
DROP TABLE troubleshooting_sessions;
DROP TABLE troubleshooting_policies;
