-- +goose Up
-- astilba: not additive: execution_operations_kind_check gains 'restore', a command kind the previous release neither issues nor reads.
-- Restoring a revoked server's backup snapshot onto the environment's current primary
-- (docs/backups.md). The source policy stays bound to the revoked server and is never written
-- again: each restore of its snapshots is recorded here instead, with the plan applied on the
-- receiving server when it was issued, which a later cutover is reviewed against.
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup','schedule','upgrade','network','crosshost','restore'));

-- id is the restore's run ID, which labels everything the agent creates for it. first_reported_at
-- is the snapshot's receipt when the restore was issued. result is what the agent reported when the
-- restore succeeded: the snapshot, the target and the manifest read out of the snapshot that the
-- restored data matched. It is not trusted for use until a cutover resets the restored data.
CREATE TABLE backup_recoveries (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, environment_id uuid NOT NULL, policy_id uuid NOT NULL,
 operation_id uuid NOT NULL UNIQUE, source_server_id uuid NOT NULL, server_id uuid NOT NULL, plan_id uuid NOT NULL,
 snapshot text NOT NULL CHECK(snapshot ~ '^[a-f0-9]{64}$'), first_reported_at timestamptz NOT NULL,
 target_volume_id uuid NOT NULL, created_at timestamptz NOT NULL,
 result jsonb CHECK(result IS NULL OR octet_length(result::text) <= 65536),
 CHECK(server_id <> source_server_id),
 FOREIGN KEY(workspace_id,environment_id,policy_id) REFERENCES backup_policies(workspace_id,environment_id,id),
 FOREIGN KEY(workspace_id,operation_id) REFERENCES execution_operations(workspace_id,id),
 FOREIGN KEY(workspace_id,source_server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,plan_id) REFERENCES visual_requests(workspace_id,environment_id,id)
);
CREATE INDEX backup_recoveries_policy ON backup_recoveries(workspace_id,environment_id,policy_id,created_at);

-- +goose Down
DROP TABLE backup_recoveries;
DELETE FROM execution_operations WHERE kind = 'restore';
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup','schedule','upgrade','network','crosshost'));
