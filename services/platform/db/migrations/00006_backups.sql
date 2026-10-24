-- +goose Up
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup'));
CREATE TABLE backup_destinations (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL REFERENCES workspaces(id),
 configuration jsonb NOT NULL, encrypted_access bytea NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE TABLE backup_policies (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, environment_id uuid NOT NULL, server_id uuid NOT NULL,
 configuration jsonb NOT NULL, encrypted_access bytea NOT NULL,
 status jsonb NOT NULL DEFAULT '{}', status_sequence bigint NOT NULL DEFAULT 0,
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 UNIQUE(workspace_id,environment_id,id)
);
