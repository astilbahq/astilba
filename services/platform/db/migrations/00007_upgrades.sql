-- +goose Up
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup','upgrade'));
CREATE TABLE supported_upgrades (
 id uuid PRIMARY KEY,
 author_id uuid NOT NULL REFERENCES accounts(id),
 workspace_id uuid NOT NULL,
 environment_id uuid NOT NULL,
 server_id uuid NOT NULL,
 plan jsonb NOT NULL,
 status jsonb NOT NULL,
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id)
);
CREATE INDEX supported_upgrades_server ON supported_upgrades(server_id);
