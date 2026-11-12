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
WHERE c.id = @id AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner';

-- name: AutomationCredentialAdmission :one
-- Revalidates an authenticated credential under the workspace write lock.
-- Keep the validity predicate identical to AuthenticateAutomationCredential.
SELECT (c.mfa_approved AND a.totp_secret IS NOT NULL)::boolean AS mfa
FROM automation_credentials c JOIN accounts a ON a.id=c.creator_id
JOIN memberships m ON m.workspace_id=c.workspace_id AND m.account_id=c.creator_id
WHERE c.id = @id AND c.workspace_id = @workspace_id AND c.environment_id = @environment_id AND c.creator_id = @creator_id
    AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner';

-- name: RevokeCreatorAutomationCredentials :many
-- A credential acts as its creator, so it ends with the creator's ownership, in the transaction
-- that ends it. Re-promotion does not bring it back.
UPDATE automation_credentials SET revoked_at=now()
WHERE workspace_id = @workspace_id AND creator_id = @creator_id AND revoked_at IS NULL AND expires_at>now()
RETURNING id;

-- name: WithdrawGitSourceApprovals :many
-- A Git source imports on an owner's approval, so it pauses when its approver stops being one,
-- until a current owner approves it again.
UPDATE git_sources SET policy_auth_version=NULL
WHERE workspace_id = @workspace_id AND policy_actor = @policy_actor AND policy_auth_version IS NOT NULL
RETURNING environment_id;

-- name: WithdrawBuildTargetApprovals :many
-- A build target's automation runs on its approver's say-so. Deploying needs an owner and building
-- an owner or operator, so when an owner becomes an operator (lost_write unset) only targets that
-- deploy pause; the others keep building but lose the owner's approval that running them on demand
-- needs. Losing write access pauses every one.
UPDATE build_targets SET policy_owner=false,
    policy_auth_version=CASE WHEN @lost_write::boolean OR COALESCE((configuration->>'autoDeploy')::boolean,false) THEN NULL ELSE policy_auth_version END
WHERE workspace_id = @workspace_id AND policy_actor = @policy_actor
    AND (policy_owner OR (policy_auth_version IS NOT NULL AND (@lost_write::boolean OR COALESCE((configuration->>'autoDeploy')::boolean,false))))
RETURNING application_id;

-- name: BlockActorAutomaticDeployments :many
-- An automatic deployment not yet planned deploys on its approver's say-so, so it stops when they
-- stop being an owner. One already queued is admitted work and finishes.
UPDATE pipeline_auto_deployments SET state='blocked',actor_auth_version=NULL,
    note='The owner who approved automatic deployment no longer approves it. An owner can approve the policy again and deploy this build.'
WHERE workspace_id = @workspace_id AND actor_id = @actor_id AND state='pending'
RETURNING id;

-- name: DisableActorPreviewPolicies :many
-- A preview policy runs on its owner's approval, so it turns off when they stop being one. A
-- current owner turns it on again, which approves it as theirs.
UPDATE preview_policies SET enabled=false
WHERE workspace_id = @workspace_id AND actor_id = @actor_id AND enabled
RETURNING id;

-- name: ApproveGitSource :execrows
-- Turning a source's automatic deployment on, or approving it, makes it the caller's: it resumes a
-- source waiting for approval.
UPDATE git_sources SET configuration=jsonb_set(configuration,'{autoDeploy}',to_jsonb(@auto_deploy::boolean)),
    policy_actor = @policy_actor,policy_auth_version = @policy_auth_version::bigint,
    status=CASE WHEN status='approval_required' THEN 'ready' ELSE status END
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: LockGitSourcePolicy :one
SELECT configuration,policy_actor FROM git_sources
WHERE workspace_id = @workspace_id AND environment_id = @environment_id FOR UPDATE;

-- name: DisableGitSourceAutoDeploy :execrows
UPDATE git_sources SET configuration=jsonb_set(configuration,'{autoDeploy}','false')
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: LockBuildTargetPolicy :one
SELECT configuration,policy_actor FROM build_targets
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND application_id = @application_id FOR UPDATE;

-- name: ApproveBuildTargetPolicy :exec
UPDATE build_targets SET policy_actor = @policy_actor,policy_auth_version = @policy_auth_version::bigint,
    policy_owner=policy_approver_holds(workspace_id,@policy_actor,@policy_auth_version::bigint,true)
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND application_id = @application_id;

-- name: AutomaticDeploymentApproved :one
-- Keep the approval check identical to the one every standing policy uses.
SELECT policy_approver_holds(workspace_id,actor_id,actor_auth_version,true)::boolean FROM pipeline_auto_deployments
WHERE workspace_id = @workspace_id AND id = @id;

-- name: WithdrawForkApprovals :many
-- A fork commit's preview approval is its approving owner's, so it ends with their ownership.
UPDATE preview_requests SET approved_head='',approved_repository=0,approved_generation=0,approved_by=NULL,approved_auth_version=NULL,next_check_at=now()
WHERE workspace_id = @workspace_id AND approved_by = @approved_by
RETURNING id;

-- name: WithdrawPreviewCleanupApprovals :many
-- A cleanup an owner asked for waits, once they stop being one, for a current owner to ask again.
UPDATE preview_requests SET cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL,next_check_at=now()
WHERE workspace_id = @workspace_id AND cleanup_actor = @cleanup_actor AND cleanup_requested AND state<>'deleted'
RETURNING id;
