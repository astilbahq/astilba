-- name: AccountByEmail :one
SELECT * FROM accounts WHERE email = $1;
-- name: AccountByID :one
SELECT * FROM accounts WHERE id = $1;
-- name: LockAccount :one
SELECT * FROM accounts WHERE id = $1 FOR UPDATE;
-- name: AccountCount :one
SELECT count(*) FROM accounts;
-- name: InsertAccount :one
INSERT INTO accounts(id, email, name, password_hash) VALUES($1,$2,$3,$4) RETURNING *;
-- name: UpdatePassword :exec
UPDATE accounts SET password_hash=$2, auth_version=auth_version+1,totp_pending=NULL,totp_pending_expiry=NULL WHERE id=$1;
-- name: BumpAuthVersion :exec
UPDATE accounts SET auth_version=auth_version+1 WHERE id=$1;
-- name: SetPendingTOTP :exec
UPDATE accounts SET totp_pending=$2, totp_pending_expiry=$3 WHERE id=$1;
-- name: ActivateTOTP :one
UPDATE accounts SET totp_secret=$3, totp_pending=NULL, totp_pending_expiry=NULL,
    totp_last_step=$2, auth_version=auth_version+1 WHERE id=$1 RETURNING auth_version;
-- name: DisableTOTP :one
UPDATE accounts SET totp_secret=NULL, totp_pending=NULL, totp_pending_expiry=NULL,
    totp_last_step=-1, auth_version=auth_version+1 WHERE id=$1 RETURNING auth_version;
-- name: AcceptTOTPStep :exec
UPDATE accounts SET totp_last_step=$2 WHERE id=$1;
-- name: InsertRecoveryCode :exec
INSERT INTO recovery_codes(account_id,code_hash) VALUES($1,$2);
-- name: DeleteRecoveryCodes :exec
DELETE FROM recovery_codes WHERE account_id=$1;
-- name: ConsumeRecoveryCode :one
DELETE FROM recovery_codes WHERE account_id=$1 AND code_hash=$2 RETURNING account_id;
-- name: CountRecoveryCodes :one
SELECT count(*) FROM recovery_codes WHERE account_id=$1;
-- name: RecordAuthAttempt :one
INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,1,$2)
ON CONFLICT(scope_hash) DO UPDATE SET
    attempts=CASE WHEN auth_attempts.expires_at <= now() THEN 1 ELSE auth_attempts.attempts+1 END,
    expires_at=CASE WHEN auth_attempts.expires_at <= now() THEN EXCLUDED.expires_at ELSE auth_attempts.expires_at END
RETURNING attempts;
-- name: ClearAuthAttempts :exec
DELETE FROM auth_attempts WHERE scope_hash=$1;
-- name: PruneAuthAttempts :exec
DELETE FROM auth_attempts WHERE expires_at < now();
-- name: InsertPasswordReset :exec
INSERT INTO password_resets(account_id,token_hash,expires_at) VALUES($1,$2,$3)
ON CONFLICT(account_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at;
-- name: LockPasswordReset :one
SELECT * FROM password_resets WHERE token_hash=$1 AND expires_at > now() FOR UPDATE;
-- name: DeletePasswordReset :exec
DELETE FROM password_resets WHERE account_id=$1;
-- name: DeleteUnsentReset :exec
DELETE FROM password_resets WHERE account_id=$1 AND token_hash=$2;
-- name: LockBootstrap :exec
SELECT pg_advisory_xact_lock(4281642301);

-- name: PeekPasswordReset :one
SELECT * FROM password_resets WHERE token_hash=$1 AND expires_at>now();
