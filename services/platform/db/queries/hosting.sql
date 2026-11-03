-- name: InsertHostedSubscription :exec
INSERT INTO hosted_subscriptions(workspace_id,server_limit) VALUES($1,$2);

-- name: MarkEmailVerified :exec
UPDATE accounts SET email_verified_at=$2 WHERE id=$1;

-- name: HostedEntitlement :one
SELECT status,management_until,server_limit FROM hosted_subscriptions WHERE workspace_id = @workspace_id;

-- name: GrantHostedManagement :execrows
-- An operator grant never replaces a paid subscription.
INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit)
VALUES(@workspace_id,'granted',@management_until,@server_limit)
ON CONFLICT(workspace_id) DO UPDATE SET status='granted',management_until = @management_until,server_limit = @server_limit,updated_at=now()
WHERE hosted_subscriptions.subscription_id IS NULL;

-- name: CountManagedServers :one
SELECT count(*) FROM servers WHERE workspace_id = @workspace_id AND NOT revoked AND id <> @exclude_id;

-- name: LockHostedCohort :exec
-- Serializes hosted invitations with the workspace creation that claims them.
SELECT pg_advisory_xact_lock(4281642302);

-- name: DeleteExpiredHostedInvitations :exec
DELETE FROM hosted_invitations WHERE claimed_at IS NULL AND expires_at<=now();

-- name: HostedCohortSize :one
-- unscoped: the hosted cohort limit counts every workspace and open invitation.
SELECT ((SELECT count(*) FROM workspaces)+(SELECT count(*) FROM hosted_invitations WHERE claimed_at IS NULL))::bigint AS size;

-- name: CountWorkspaces :one
-- unscoped: the hosted cohort limit counts every workspace.
SELECT count(*) FROM workspaces;

-- name: InsertHostedInvitation :exec
INSERT INTO hosted_invitations(id,email,token_hash,expires_at) VALUES(@id,@email,@token_hash,@expires_at);

-- name: DeleteUnsentHostedInvitation :exec
-- unscoped: the invitation token identifies an invitation that has no workspace yet.
DELETE FROM hosted_invitations WHERE token_hash = @token_hash AND claimed_at IS NULL;

-- name: HostedInvitationOpen :one
-- unscoped: the invitation token is checked before its workspace exists.
SELECT EXISTS(SELECT 1 FROM hosted_invitations
    WHERE token_hash = @token_hash AND claimed_at IS NULL AND expires_at > @valid_at)::boolean;

-- name: LockHostedInvitation :one
-- unscoped: the invitation token establishes the invited email before its workspace exists.
SELECT id,email FROM hosted_invitations
WHERE token_hash = @token_hash AND claimed_at IS NULL AND expires_at > @valid_at FOR UPDATE;

-- name: ClaimHostedInvitation :exec
UPDATE hosted_invitations SET claimed_at = @claimed_at WHERE id = @id;

-- name: UpsertEmailVerification :exec
INSERT INTO email_verifications(account_id,token_hash,expires_at) VALUES(@account_id,@token_hash,@expires_at)
ON CONFLICT(account_id) DO UPDATE SET token_hash = @token_hash,expires_at = @expires_at;

-- name: DeleteUnsentEmailVerification :exec
DELETE FROM email_verifications WHERE account_id = @account_id AND token_hash = @token_hash;

-- name: EmailVerificationOpen :one
SELECT EXISTS(SELECT 1 FROM email_verifications
    WHERE account_id = @account_id AND token_hash = @token_hash AND expires_at > @valid_at)::boolean;

-- name: ConsumeEmailVerification :one
DELETE FROM email_verifications
WHERE account_id = @account_id AND token_hash = @token_hash AND expires_at > @valid_at
RETURNING account_id;
