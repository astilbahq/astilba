-- name: RefreshSession :execrows
-- unscoped: the hashed browser session token is the credential.
-- UPDATE serializes with logout, renewal or expiry cleanup and never recreates
-- a deleted token. A session that expired while its request ran stays expired.
UPDATE sessions SET data = @data,expiry = @expiry WHERE token = @token AND expiry > clock_timestamp();

-- name: FindSession :one
-- unscoped: the hashed browser session token is the credential.
SELECT data,lineage FROM sessions WHERE token = @token AND current_timestamp < expiry;

-- name: InsertSession :exec
-- unscoped: the hashed browser session token is the credential.
INSERT INTO sessions(token,data,expiry,lineage) VALUES(@token,@data,@expiry,@lineage);

-- name: LockSessionLineage :exec
-- unscoped: a lineage is one browser's chain of renewed session tokens.
-- Orders a renewal and a logout of the same lineage, so that the logout's delete sees a
-- token the renewal committed. The seed keeps these keys apart from the fixed ones.
SELECT pg_advisory_xact_lock(hashtextextended(CAST(sqlc.arg(lineage)::uuid AS text), 4281642304));

-- name: DeleteRenewedSession :execrows
-- unscoped: the hashed browser session token is the credential.
-- A session that expired while its request ran is not renewed: its row may outlive the expiry
-- until cleanup, and a logout no longer sees it.
DELETE FROM sessions WHERE token = @token AND expiry > clock_timestamp();

-- name: DeleteSessionLineage :exec
-- unscoped: a lineage is one browser's chain of renewed session tokens.
DELETE FROM sessions WHERE lineage = @lineage;

-- name: MoveTroubleshootingBrowserBinding :exec
-- unscoped: the hashed browser session token identifies the sessions it opened.
UPDATE troubleshooting_sessions SET browser_session_hash = @renewed
WHERE browser_session_hash = @previous AND state <> 'closed';

-- name: BrowserSessionAdmission :one
-- unscoped: the hashed browser session token is the credential.
-- Revalidates an authenticated session under the lock its request waited for.
-- Keep the validity predicate identical to BrowserSessionSnapshotAdmission.
SELECT a.id FROM accounts a JOIN sessions s ON s.token = @token AND s.expiry > clock_timestamp()
WHERE a.id = @account_id AND a.auth_version = @auth_version;

-- name: BrowserSessionSnapshotAdmission :one
-- unscoped: the hashed browser session token is the credential.
-- Fails to serialize if the account changed or the session was deleted after the
-- transaction's snapshot. KEY SHARE ignores a session's idle refresh.
-- Keep the validity predicate identical to BrowserSessionAdmission.
SELECT a.id FROM accounts a JOIN sessions s ON s.token = @token AND s.expiry > clock_timestamp()
WHERE a.id = @account_id AND a.auth_version = @auth_version
FOR SHARE OF a FOR KEY SHARE OF s;
