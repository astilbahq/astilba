-- +goose Up
CREATE TABLE servers (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 role text NOT NULL CHECK(role IN ('deployment','build','combined')),
 enrollment_hash bytea, enrollment_expires timestamptz, credential_hash bytea,
 previous_hash bytea, previous_until timestamptz, machine_id uuid,
 revoked boolean NOT NULL DEFAULT false, rotation_requested boolean NOT NULL DEFAULT false,
 capabilities jsonb, preflight jsonb, last_seen timestamptz,
 poll_id uuid, poll_until timestamptz, update_generation bigint NOT NULL DEFAULT 0,
 UNIQUE(workspace_id,id)
);
ALTER TABLE environments ADD COLUMN execution_generation bigint NOT NULL DEFAULT 0;
CREATE TABLE execution_plans (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, server_id uuid NOT NULL,
 environment_id uuid NOT NULL, revision_id uuid NOT NULL,
 action text NOT NULL CHECK(action IN ('start','inspect','remove')), resource_id uuid NOT NULL,
 image text NOT NULL, hash text NOT NULL, expires_at timestamptz NOT NULL,
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
CREATE TRIGGER execution_plan_immutable BEFORE UPDATE OR DELETE ON execution_plans FOR EACH ROW EXECUTE FUNCTION immutable_specification();
CREATE TABLE execution_operations (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, server_id uuid NOT NULL,
 environment_id uuid, generation bigint NOT NULL, plan_id uuid UNIQUE REFERENCES execution_plans(id),
 request_key uuid NOT NULL, command jsonb NOT NULL, kind text NOT NULL CHECK(kind IN ('test','update')),
 state text NOT NULL CHECK(state IN ('queued','dispatched','running','needs_attention','succeeded','failed','cancelled')),
 cancel_requested boolean NOT NULL DEFAULT false,
 report_sequence bigint NOT NULL DEFAULT 0, steps jsonb NOT NULL DEFAULT '[]', note text NOT NULL DEFAULT '',
 job_id bigint, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 UNIQUE(workspace_id,request_key), UNIQUE(workspace_id,environment_id,generation)
);
CREATE UNIQUE INDEX execution_active_environment ON execution_operations(workspace_id,environment_id) WHERE state NOT IN ('succeeded','failed','cancelled');
CREATE UNIQUE INDEX execution_active_server ON execution_operations(server_id) WHERE state NOT IN ('succeeded','failed','cancelled');
CREATE INDEX execution_history ON execution_operations(workspace_id,created_at DESC);
