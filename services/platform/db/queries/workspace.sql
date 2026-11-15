-- name: InsertWorkspace :exec
INSERT INTO workspaces(id,name) VALUES($1,$2);
-- name: InsertMembership :exec
INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,$3);
-- name: ListWorkspaces :many
SELECT w.*,m.role FROM workspaces w JOIN memberships m ON m.workspace_id=w.id WHERE m.account_id=$1 ORDER BY w.created_at,w.id;
-- name: Workspace :one
SELECT w.*,m.role FROM workspaces w JOIN memberships m ON m.workspace_id=w.id WHERE w.id=$1 AND m.account_id=$2;
-- name: WorkspaceForSnapshot :one
SELECT w.*,m.role FROM workspaces w JOIN memberships m ON m.workspace_id=w.id WHERE w.id=$1 AND m.account_id=$2 FOR SHARE OF m;
-- name: LockWorkspaceByID :one
-- FOR NO KEY UPDATE serializes writers without making inserts that reference the
-- workspace, such as a read's audit event, wait for the lock on a connection.
SELECT * FROM workspaces WHERE id=$1 FOR NO KEY UPDATE;
-- name: SetMFAPolicy :exec
UPDATE workspaces SET require_mfa=$2 WHERE id=$1;
-- name: ListMembers :many
SELECT a.id,a.email,a.name,(a.totp_secret IS NOT NULL)::boolean AS mfa_enabled,m.role
FROM accounts a JOIN memberships m ON m.account_id=a.id WHERE m.workspace_id=$1 ORDER BY a.email;
-- name: OwnerCount :one
SELECT count(*) FROM memberships WHERE workspace_id=$1 AND role='owner';
-- name: GetMembership :one
SELECT * FROM memberships WHERE workspace_id=$1 AND account_id=$2;
-- name: UpdateMembership :exec
UPDATE memberships SET role=$3 WHERE workspace_id=$1 AND account_id=$2;
-- name: DeleteMembership :exec
DELETE FROM memberships WHERE workspace_id=$1 AND account_id=$2;
-- name: InsertInvitation :exec
INSERT INTO invitations(workspace_id,id,email,role,token_hash,expires_at,issuer_id,issuer_auth_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(workspace_id,email) DO UPDATE SET id=EXCLUDED.id,role=EXCLUDED.role,token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,
    issuer_id=EXCLUDED.issuer_id,issuer_auth_version=EXCLUDED.issuer_auth_version;
-- name: LockInvitation :one
-- unscoped: the presented invitation token establishes the workspace.
-- An invitation counts only while its issuer is still an owner with the same authentication
-- version.
SELECT * FROM invitations
WHERE token_hash=$1 AND expires_at > now() AND policy_approver_holds(workspace_id,issuer_id,issuer_auth_version,true) FOR UPDATE;
-- name: PeekInvitation :one
-- unscoped: the presented invitation token establishes the workspace.
SELECT * FROM invitations
WHERE token_hash=$1 AND expires_at > now() AND policy_approver_holds(workspace_id,issuer_id,issuer_auth_version,true);
-- name: ListInvitations :many
SELECT id,email,role,expires_at FROM invitations
WHERE workspace_id=$1 AND expires_at>now() AND policy_approver_holds(workspace_id,issuer_id,issuer_auth_version,true) ORDER BY email;
-- name: WithdrawIssuerInvitations :many
-- An owner's invitations end with their ownership, in the transaction that ends it.
DELETE FROM invitations WHERE workspace_id = @workspace_id AND issuer_id = @issuer_id RETURNING id;
-- name: DeleteInvitation :exec
DELETE FROM invitations WHERE workspace_id=$1 AND id=$2;
-- name: InsertAudit :exec
INSERT INTO audit_events(workspace_id,id,actor_id,action,resource_id,automation_credential_id) VALUES($1,$2,$3,$4,$5,$6);
-- name: ListAudit :many
SELECT * FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;
-- name: SetWorkspaceSettings :exec
UPDATE workspaces SET name=$2,require_mfa=$3 WHERE id=$1;

-- name: MFARequirementApprovals :many
-- The standing approvals and deferred actions that act only while the workspace doesn't require
-- two-factor authentication: each holds apart from that requirement (approver_role_holds), and its
-- approver hasn't set up two-factor authentication. So an owner sees what requiring it would stop,
-- or, while it is required, what turning it off would let act again. A deferred action that came
-- due while it was required was refused for good, so it is no longer pending and isn't listed.
-- Retired environments' sources, targets and previews run nothing, so they are left out, and so are
-- a build target that neither builds automatically nor has an owner's approval for automation to
-- run it, and a fork commit's approval once the pull request or preview policy has moved on. An automation credential whose creator lacks two-factor authentication keeps reading
-- but can't write.
WITH approvals(kind,approver,environment_id,subject) AS (
    SELECT 'gitSource',g.policy_actor,g.environment_id,''
    FROM git_sources g JOIN environments e ON e.workspace_id=g.workspace_id AND e.id=g.environment_id
    WHERE g.workspace_id = @workspace_id AND e.state='active' AND g.status<>'approval_required'
        AND approver_role_holds(g.workspace_id,g.policy_actor,g.policy_auth_version,true)
    UNION ALL
    SELECT 'buildTarget',b.policy_actor,b.environment_id,COALESCE((
        SELECT x->>'name' FROM specification_revisions r, jsonb_array_elements(r.specification->'applications') x
        WHERE r.workspace_id=e.workspace_id AND r.id=e.draft_revision_id AND x->>'id'=b.application_id::text LIMIT 1),'')
    FROM build_targets b JOIN environments e ON e.workspace_id=b.workspace_id AND e.id=b.environment_id
    WHERE b.workspace_id = @workspace_id AND e.state='active'
        AND (COALESCE((b.configuration->>'autoBuild')::boolean,false) OR b.policy_owner)
        AND approver_role_holds(b.workspace_id,b.policy_actor,b.policy_auth_version,COALESCE((b.configuration->>'autoDeploy')::boolean,false))
    UNION ALL
    SELECT 'automaticDeployment',d.actor_id,d.environment_id,''
    FROM pipeline_auto_deployments d
    WHERE d.workspace_id = @workspace_id AND d.state='pending'
        AND approver_role_holds(d.workspace_id,d.actor_id,d.actor_auth_version,true)
    UNION ALL
    SELECT 'previewPolicy',p.actor_id,p.environment_id,''
    FROM preview_policies p JOIN environments e ON e.workspace_id=p.workspace_id AND e.id=p.environment_id
    WHERE p.workspace_id = @workspace_id AND p.enabled AND e.state='active'
        AND approver_role_holds(p.workspace_id,p.actor_id,p.auth_version,true)
    UNION ALL
    SELECT 'previewApproval',r.approved_by,p.environment_id,'#'||r.number
    FROM preview_requests r JOIN preview_policies p ON p.workspace_id=r.workspace_id AND p.id=r.policy_id
        JOIN environments e ON e.workspace_id=p.workspace_id AND e.id=p.environment_id
    WHERE r.workspace_id = @workspace_id AND r.fork AND r.state<>'deleted' AND p.enabled AND e.state='active'
        AND r.approved_head=r.head AND r.approved_repository=r.head_repository AND r.approved_generation=p.generation
        AND approver_role_holds(r.workspace_id,r.approved_by,r.approved_auth_version,true)
    UNION ALL
    SELECT 'previewCleanup',r.cleanup_actor,p.environment_id,'#'||r.number
    FROM preview_requests r JOIN preview_policies p ON p.workspace_id=r.workspace_id AND p.id=r.policy_id
    WHERE r.workspace_id = @workspace_id AND r.cleanup_requested AND r.state<>'deleted'
        AND approver_role_holds(r.workspace_id,r.cleanup_actor,r.cleanup_auth_version,true)
    UNION ALL
    SELECT 'invitation',i.issuer_id,NULL::uuid,i.email
    FROM invitations i
    WHERE i.workspace_id = @workspace_id AND i.expires_at>now()
        AND approver_role_holds(i.workspace_id,i.issuer_id,i.issuer_auth_version,true)
    UNION ALL
    SELECT 'enrollmentCode',s.enrollment_issuer_id,NULL::uuid,s.name
    FROM servers s
    WHERE s.workspace_id = @workspace_id AND s.enrollment_hash IS NOT NULL AND s.enrollment_expires>now()
        AND s.credential_hash IS NULL AND NOT s.revoked
        AND approver_role_holds(s.workspace_id,s.enrollment_issuer_id,s.enrollment_issuer_auth_version,true)
    UNION ALL
    SELECT 'sourceSubmission',q.actor_id,q.environment_id,''
    FROM source_submissions q
    WHERE q.workspace_id = @workspace_id AND q.state='queued' AND q.withdrawn_at IS NULL
        AND approver_role_holds(q.workspace_id,q.actor_id,q.actor_auth_version,false)
    UNION ALL
    SELECT 'automationCredential',c.creator_id,c.environment_id,c.name
    FROM automation_credentials c
    WHERE c.workspace_id = @workspace_id AND c.revoked_at IS NULL AND c.expires_at>now()
        AND NOT c.capabilities <@ ARRAY['environment:read']::text[]
        AND approver_role_holds(c.workspace_id,c.creator_id,c.auth_version,true)
)
SELECT x.kind::text AS kind,a.id AS approver_id,a.name AS approver_name,a.email AS approver_email,
    p.id AS project_id,p.name AS project_name,e.id AS environment_id,e.name AS environment_name,x.subject::text AS subject
FROM approvals x JOIN accounts a ON a.id=x.approver
LEFT JOIN environments e ON e.workspace_id = @workspace_id AND e.id=x.environment_id
LEFT JOIN projects p ON p.workspace_id = @workspace_id AND p.id=e.project_id
WHERE a.totp_secret IS NULL
ORDER BY a.email,x.kind,p.name,e.name,x.subject;
