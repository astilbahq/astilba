-- name: CountActiveAutomationCredentials :one
SELECT count(*) FROM automation_credentials
WHERE workspace_id = @workspace_id AND revoked_at IS NULL AND expires_at>now();

-- name: InsertAutomationCredential :exec
INSERT INTO automation_credentials(id,workspace_id,environment_id,creator_id,name,secret_hash,auth_version,mfa_approved,capabilities,created_at,expires_at)
VALUES(@id,@workspace_id,@environment_id,@creator_id,@name,@secret_hash,@auth_version,@mfa_approved,@capabilities,@created_at,@expires_at);

-- name: ListAutomationCredentials :many
SELECT id,creator_id,name,capabilities,created_at,expires_at,revoked_at FROM automation_credentials
WHERE workspace_id = @workspace_id AND environment_id = @environment_id
ORDER BY (revoked_at IS NULL AND expires_at>now()) DESC,created_at DESC,id LIMIT 200;

-- name: RevokeAutomationCredential :execrows
UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,now())
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: AuthenticateAutomationCredential :one
-- unscoped: the presented automation credential establishes the workspace.
-- Keep the validity predicate identical to AutomationCredentialAdmission. The
-- caller compares secret_hash in constant time.
SELECT c.workspace_id,e.project_id,c.environment_id,c.creator_id,c.secret_hash,c.capabilities,c.expires_at
FROM automation_credentials c JOIN environments e ON e.workspace_id=c.workspace_id AND e.id=c.environment_id
JOIN accounts a ON a.id=c.creator_id JOIN memberships m ON m.workspace_id=c.workspace_id AND m.account_id=c.creator_id
WHERE c.id = @id AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner'
    AND e.state='active';

-- name: AutomationCredentialAdmission :one
-- Revalidates an authenticated credential under the workspace write lock.
-- Keep the validity predicate identical to AuthenticateAutomationCredential.
SELECT (c.mfa_approved AND a.totp_secret IS NOT NULL)::boolean AS mfa
FROM automation_credentials c JOIN accounts a ON a.id=c.creator_id
JOIN memberships m ON m.workspace_id=c.workspace_id AND m.account_id=c.creator_id
JOIN environments e ON e.workspace_id=c.workspace_id AND e.id=c.environment_id
WHERE c.id = @id AND c.workspace_id = @workspace_id AND c.environment_id = @environment_id AND c.creator_id = @creator_id
    AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner'
    AND e.state='active';
