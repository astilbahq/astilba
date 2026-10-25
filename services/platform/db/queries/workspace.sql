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
-- name: LockWorkspace :one
SELECT w.*,m.role FROM workspaces w JOIN memberships m ON m.workspace_id=w.id WHERE w.id=$1 AND m.account_id=$2 FOR UPDATE OF w;
-- name: LockWorkspaceByID :one
SELECT * FROM workspaces WHERE id=$1 FOR UPDATE;
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
INSERT INTO invitations(workspace_id,id,email,role,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(workspace_id,email) DO UPDATE SET id=EXCLUDED.id,role=EXCLUDED.role,token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at;
-- name: LockInvitation :one
SELECT * FROM invitations WHERE token_hash=$1 AND expires_at > now() FOR UPDATE;
-- name: PeekInvitation :one
SELECT * FROM invitations WHERE token_hash=$1 AND expires_at > now();
-- name: ListInvitations :many
SELECT id,email,role,expires_at FROM invitations WHERE workspace_id=$1 AND expires_at>now() ORDER BY email;
-- name: DeleteInvitation :exec
DELETE FROM invitations WHERE workspace_id=$1 AND id=$2;
-- name: InsertAudit :exec
INSERT INTO audit_events(workspace_id,id,actor_id,action,resource_id,automation_credential_id) VALUES($1,$2,$3,$4,$5,$6);
-- name: ListAudit :many
SELECT * FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;
-- name: SetWorkspaceSettings :exec
UPDATE workspaces SET name=$2,require_mfa=$3 WHERE id=$1;
