-- name: ListGitConnections :many
SELECT c.configuration,COALESCE(a.active,false)::boolean AS app_active,COALESCE(a.app_id,0)::bigint AS app_id
FROM git_connections c LEFT JOIN github_app_connections a ON a.workspace_id=c.workspace_id AND a.connection_id=c.id
WHERE c.workspace_id = @workspace_id ORDER BY c.id LIMIT 100;

-- name: CountGitConnections :one
SELECT count(*) FROM git_connections WHERE workspace_id = @workspace_id;

-- name: InsertGitConnection :exec
INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access)
VALUES(@id,@workspace_id,@configuration,@encrypted_access);

-- name: GetGitConnection :one
SELECT configuration,encrypted_access FROM git_connections WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockGitConnection :one
SELECT configuration FROM git_connections WHERE workspace_id = @workspace_id AND id = @id FOR UPDATE;

-- name: GitConnectionForWebhook :one
-- unscoped: the webhook URL names the connection, whose stored secret then
-- authenticates the delivery and establishes the workspace.
SELECT workspace_id,configuration,encrypted_access FROM git_connections WHERE id = @id;

-- name: LockGitConnectionDeliveries :exec
-- Bounds delivery retention and coalesced refresh scheduling for a connection.
SELECT id FROM git_connections WHERE workspace_id = @workspace_id AND id = @id FOR UPDATE;

-- name: GitDeliveryBodyHash :one
SELECT body_hash FROM git_deliveries
WHERE workspace_id = @workspace_id AND connection_id = @connection_id AND delivery_id = @delivery_id;

-- name: InsertGitDelivery :exec
INSERT INTO git_deliveries(workspace_id,connection_id,delivery_id,body_hash)
VALUES(@workspace_id,@connection_id,@delivery_id,@body_hash);

-- name: PruneGitDeliveries :exec
DELETE FROM git_deliveries d
WHERE d.workspace_id = @workspace_id AND d.connection_id = @connection_id AND d.delivery_id NOT IN (
    SELECT k.delivery_id FROM git_deliveries k WHERE k.workspace_id = @workspace_id AND k.connection_id = @connection_id
    ORDER BY k.received_at DESC LIMIT 1000);

-- name: ListRegistries :many
SELECT configuration FROM registries WHERE workspace_id = @workspace_id ORDER BY id LIMIT 100;

-- name: CountRegistries :one
SELECT count(*) FROM registries WHERE workspace_id = @workspace_id;

-- name: InsertRegistry :exec
INSERT INTO registries(id,workspace_id,configuration,encrypted_access)
VALUES(@id,@workspace_id,@configuration,@encrypted_access);

-- name: GetRegistry :one
SELECT configuration,encrypted_access FROM registries WHERE workspace_id = @workspace_id AND id = @id;

-- name: ListBuildSecrets :many
SELECT id,name,created_at FROM build_secret_versions WHERE workspace_id = @workspace_id ORDER BY created_at DESC LIMIT 100;

-- name: CountBuildSecrets :one
SELECT count(*) FROM build_secret_versions WHERE workspace_id = @workspace_id;

-- name: InsertBuildSecret :one
INSERT INTO build_secret_versions(id,workspace_id,name,encrypted_value)
VALUES(@id,@workspace_id,@name,@encrypted_value) RETURNING created_at;

-- name: GetBuildSecretValue :one
SELECT encrypted_value FROM build_secret_versions WHERE workspace_id = @workspace_id AND id = @id;

-- name: ListBuildRejections :many
SELECT application_id,rejected_commit::text AS rejected_commit,rejection_reason::text AS rejection_reason,rejected_at::timestamptz AS rejected_at FROM build_targets
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND rejected_at IS NOT NULL
ORDER BY application_id;

-- name: PageEnvironmentSecretVersions :many
SELECT v.id,v.encrypted_value FROM secret_versions v
JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id
WHERE v.workspace_id = @workspace_id AND s.environment_id = @environment_id AND v.id > @after_id
ORDER BY v.id LIMIT @page_size;

-- name: DeleteEnvironmentSecretVersions :exec
DELETE FROM secret_versions v USING secrets s
WHERE s.workspace_id = @workspace_id AND s.environment_id = @environment_id
    AND v.workspace_id=s.workspace_id AND v.secret_id=s.id;

-- name: DeleteEnvironmentSecrets :exec
DELETE FROM secrets WHERE workspace_id = @workspace_id AND environment_id = @environment_id;
