-- +goose Up
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline'));
DROP INDEX execution_active_server;
CREATE UNIQUE INDEX execution_active_server ON execution_operations(server_id)
 WHERE state NOT IN ('succeeded','failed','cancelled') AND (kind<>'pipeline' OR state<>'queued');

CREATE TABLE git_connections (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 configuration jsonb NOT NULL, encrypted_access bytea NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE TABLE registries (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 configuration jsonb NOT NULL, encrypted_access bytea NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE TABLE build_secret_versions (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 encrypted_value bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id)
);
CREATE TABLE git_sources (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL,
 connection_id uuid NOT NULL, configuration jsonb NOT NULL,
 imported_revision_id uuid, imported_commit text NOT NULL DEFAULT '',
 pending_commit text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'pending',
 policy_actor uuid NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(workspace_id,environment_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,imported_revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
CREATE TABLE build_targets (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, application_id uuid NOT NULL,
 connection_id uuid NOT NULL, configuration jsonb NOT NULL, input_hash text NOT NULL,
 requested_commit text NOT NULL DEFAULT '', policy_actor uuid NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(workspace_id,environment_id,application_id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id)
);
CREATE TABLE pipeline_runs (
 id uuid PRIMARY KEY REFERENCES execution_operations(id), workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL, application_id uuid NOT NULL, revision_id uuid NOT NULL,
 configuration jsonb NOT NULL, result jsonb, artifact_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,environment_id,id),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
CREATE TABLE build_artifacts (
 id uuid PRIMARY KEY REFERENCES pipeline_runs(id), workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL, identity jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id)
);
CREATE TRIGGER artifact_immutable BEFORE UPDATE OR DELETE ON build_artifacts FOR EACH ROW EXECUTE FUNCTION immutable_specification();
CREATE TABLE git_deliveries (
 workspace_id uuid NOT NULL, connection_id uuid NOT NULL, delivery_id text NOT NULL,
 body_hash text NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(connection_id,delivery_id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id)
);
CREATE TABLE git_submissions (
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL, revision_id uuid NOT NULL,
 operation_id uuid NOT NULL REFERENCES pipeline_runs(id), request_key uuid NOT NULL,
 PRIMARY KEY(workspace_id,environment_id,revision_id), UNIQUE(workspace_id,request_key),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
CREATE TABLE pipeline_auto_deployments (
 id uuid PRIMARY KEY REFERENCES pipeline_runs(id),
 workspace_id uuid NOT NULL, environment_id uuid NOT NULL,
 revision_id uuid NOT NULL, actor_id uuid NOT NULL REFERENCES accounts(id),
 plan_id uuid, operation_id uuid REFERENCES execution_operations(id),
 state text NOT NULL DEFAULT 'pending', note text NOT NULL DEFAULT '',
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
