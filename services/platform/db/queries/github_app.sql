-- name: DeleteExpiredGitHubAuthorizations :exec
DELETE FROM github_app_authorizations WHERE workspace_id = @workspace_id AND expires_at<now();

-- name: CountGitHubAuthorizations :one
SELECT count(*) AS total,count(*) FILTER(WHERE account_id = @account_id AND status IN ('pending','ready')) AS active
FROM github_app_authorizations WHERE workspace_id = @workspace_id;

-- name: InsertGitHubAuthorization :exec
INSERT INTO github_app_authorizations(id,workspace_id,account_id,auth_version,session_hash,state_hash,app_id,encrypted_access,status,expires_at)
VALUES(@id,@workspace_id,@account_id,@auth_version,@session_hash,@state_hash,@app_id,@encrypted_access,'pending',now()+interval '10 minutes');

-- name: GitHubAuthorizationByState :one
-- unscoped: the OAuth callback state establishes the workspace. The caller then
-- requires the authorizing account and session with LockGitHubAuthorization.
SELECT workspace_id,id FROM github_app_authorizations WHERE state_hash = @state_hash;

-- name: LockGitHubAuthorization :one
-- The grant predicate: the authorizing account at its current auth version, the
-- same login session, this controller's App and an unexpired row. Callers check
-- status.
SELECT encrypted_access,status,COALESCE(github_user_id,0)::bigint AS github_user_id,github_login,expires_at
FROM github_app_authorizations
WHERE workspace_id = @workspace_id AND id = @id AND account_id = @account_id AND auth_version = @auth_version
    AND session_hash = @session_hash AND app_id = @app_id AND expires_at>now()
FOR UPDATE;

-- name: CompleteGitHubAuthorization :one
UPDATE github_app_authorizations SET encrypted_access = @encrypted_access,status = @status,
    github_user_id = @github_user_id::bigint,github_login = @github_login
WHERE workspace_id = @workspace_id AND id = @id
RETURNING id,workspace_id,expires_at;

-- name: GitHubAuthorizationUser :one
SELECT github_user_id::bigint AS github_user_id,github_login FROM github_app_authorizations WHERE workspace_id = @workspace_id AND id = @id;

-- name: ConsumeGitHubAuthorization :exec
UPDATE github_app_authorizations SET status='consumed',encrypted_access = @encrypted_access
WHERE workspace_id = @workspace_id AND id = @id;

-- name: UpsertGitHubAppGitConnection :execrows
-- A connection ID owned by another workspace updates no row.
INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access)
VALUES(@id,@workspace_id,@configuration,@encrypted_access)
ON CONFLICT(id) DO UPDATE SET configuration=EXCLUDED.configuration,encrypted_access=EXCLUDED.encrypted_access
WHERE git_connections.workspace_id=EXCLUDED.workspace_id;

-- name: UpsertGitHubAppConnection :execrows
-- A connection ID owned by another workspace updates no row.
INSERT INTO github_app_connections(workspace_id,connection_id,app_id,installation_id,repository_id,github_user_id,actor_id)
VALUES(@workspace_id,@connection_id,@app_id,@installation_id,@repository_id,@github_user_id,@actor_id)
ON CONFLICT(connection_id) DO UPDATE SET app_id=EXCLUDED.app_id,installation_id=EXCLUDED.installation_id,
    repository_id=EXCLUDED.repository_id,github_user_id=EXCLUDED.github_user_id,actor_id=EXCLUDED.actor_id,
    generation=github_app_connections.generation+1,active=true
WHERE github_app_connections.workspace_id=EXCLUDED.workspace_id;

-- name: DeactivateGitHubAppConnection :exec
UPDATE github_app_connections SET active=false,generation=generation+1
WHERE workspace_id = @workspace_id AND connection_id = @connection_id;

-- name: ShareActiveGitHubAppConnection :one
SELECT app_id,installation_id,repository_id,generation FROM github_app_connections
WHERE workspace_id = @workspace_id AND connection_id = @connection_id AND active FOR SHARE;

-- name: CancelGitHubOperations :exec
-- Match Report: operation rows precede source rows and the App binding.
UPDATE execution_operations SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END
WHERE workspace_id = @workspace_id AND kind='pipeline' AND command->'pipeline'->'connection'->>'id' = @connection_id::text
    AND state NOT IN ('succeeded','failed','cancelled');

-- name: PauseGitHubSources :exec
UPDATE git_sources SET status='approval_required',configuration=jsonb_set(configuration,'{autoDeploy}','false')
WHERE workspace_id = @workspace_id AND connection_id = @connection_id;

-- name: PauseGitHubBuildTargets :exec
UPDATE build_targets SET configuration=jsonb_set(jsonb_set(configuration,'{autoBuild}','false'),'{autoDeploy}','false')
WHERE workspace_id = @workspace_id AND connection_id = @connection_id;
