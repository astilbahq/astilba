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

-- name: AutomaticBuildArtifact :one
SELECT identity FROM build_artifacts WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: EnvironmentHoldsAutomaticWork :one
-- An automatic deployment has no active operation between its passes, and a build admitted in that gap
-- would stack its image on a draft still waiting to deploy. The age limit keeps a job stuck in retries
-- from stopping automatic work for the environment indefinitely; a run admitted after it blocks for review.
SELECT (EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.environment_id = @environment_id::uuid AND o.state NOT IN ('succeeded','failed','cancelled'))
    OR EXISTS(SELECT 1 FROM pipeline_auto_deployments d JOIN pipeline_runs r ON r.workspace_id = d.workspace_id AND r.id = d.id
              WHERE d.workspace_id = @workspace_id AND d.environment_id = @environment_id::uuid AND d.state = 'pending' AND r.created_at > now() - interval '1 hour'))::boolean AS busy;

-- name: AutomaticDeploymentWorkspace :one
-- unscoped: queued jobs carry only the deployment, as the other job lookups by ID do; the row
-- establishes the workspace. Remove this when jobs carry the workspace in their arguments.
SELECT workspace_id FROM pipeline_auto_deployments WHERE id = @id;
