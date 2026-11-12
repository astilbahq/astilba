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
