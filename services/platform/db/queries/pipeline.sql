-- name: AllGitAccess :many
SELECT workspace_id,id,encrypted_access FROM git_connections FOR UPDATE;
-- name: RewrapGitAccess :exec
UPDATE git_connections SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllRegistryAccess :many
SELECT workspace_id,id,encrypted_access FROM registries FOR UPDATE;
-- name: RewrapRegistryAccess :exec
UPDATE registries SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllBuildSecrets :many
SELECT workspace_id,id,encrypted_value FROM build_secret_versions FOR UPDATE;
-- name: RewrapBuildSecret :exec
UPDATE build_secret_versions SET encrypted_value=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllGitHubAuthorizations :many
SELECT workspace_id,id,encrypted_access FROM github_app_authorizations FOR UPDATE;
-- name: RewrapGitHubAuthorization :exec
UPDATE github_app_authorizations SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;

-- name: MarkPipelineDeliveryStarted :exec
UPDATE pipeline_runs SET delivery_started=true WHERE workspace_id = @workspace_id AND id = @id;
-- name: FinishUndeliveredPipeline :exec
UPDATE execution_operations SET state = @state, note = @note WHERE workspace_id = @workspace_id AND id = @id;
-- name: StopPossiblyDeliveredPipeline :exec
UPDATE execution_operations SET cancel_requested = true, note = @note::text || CASE WHEN state = 'needs_attention' AND note <> '' THEN ' ' || note ELSE '' END WHERE workspace_id = @workspace_id AND id = @id;

-- name: AutomaticDeploymentWorkspace :one
-- unscoped: queued jobs carry only the deployment, as the other job lookups by ID do; the row
-- establishes the workspace. Remove this when jobs carry the workspace in their arguments.
SELECT workspace_id FROM pipeline_auto_deployments WHERE id = @id;
