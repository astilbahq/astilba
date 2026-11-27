-- name: RefreshSession :execrows
-- unscoped: the hashed browser session token is the credential.
-- UPDATE serializes with logout, renewal or expiry cleanup and never recreates
-- a deleted token. A session that expired while its request ran stays expired.
-- A signed-in session is last seen now, from the client this request summarized.
UPDATE sessions SET data = @data,expiry = @expiry,
    last_seen_at = CASE WHEN account_id IS NULL THEN NULL ELSE clock_timestamp() END,
    client_browser = CASE WHEN account_id IS NULL THEN NULL ELSE CAST(@client_browser AS text) END,
    client_os = CASE WHEN account_id IS NULL THEN NULL ELSE CAST(@client_os AS text) END,
    client_network = CASE WHEN account_id IS NULL THEN NULL ELSE sqlc.narg(client_network)::text END
WHERE token = @token AND expiry > clock_timestamp();

-- name: FindSession :one
-- unscoped: the hashed browser session token is the credential.
SELECT data,lineage FROM sessions WHERE token = @token AND current_timestamp < expiry;

-- name: InsertSession :exec
-- unscoped: the hashed browser session token is the credential.
-- A session signed in to an account records it, its version and its client, and is last seen
-- now. It signed in at signed_in_at, which a renewal carries over, or now.
INSERT INTO sessions(token,data,expiry,lineage,account_id,auth_version,signed_in_at,last_seen_at,client_browser,client_os,client_network)
SELECT @token,@data,@expiry,@lineage,i.account_id,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE sqlc.narg(auth_version)::bigint END,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE coalesce(sqlc.narg(signed_in_at)::timestamptz, clock_timestamp()) END,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE clock_timestamp() END,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE CAST(@client_browser AS text) END,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE CAST(@client_os AS text) END,
    CASE WHEN i.account_id IS NULL THEN NULL ELSE sqlc.narg(client_network)::text END
FROM (SELECT sqlc.narg(account_id)::uuid AS account_id) i;

-- name: LockSessionLineage :exec
-- unscoped: a lineage is one browser's chain of renewed session tokens.
-- Orders a renewal and a logout of the same lineage, so that the logout's delete sees a
-- token the renewal committed. The seed keeps these keys apart from the fixed ones.
SELECT pg_advisory_xact_lock(hashtextextended(CAST(sqlc.arg(lineage)::uuid AS text), 4281642304));

-- name: DeleteRenewedSession :one
-- unscoped: the hashed browser session token is the credential.
-- A session that expired while its request ran is not renewed: its row may outlive the expiry
-- until cleanup, and a logout no longer sees it. Returns when the session signed in, which its
-- renewal keeps, if it had.
DELETE FROM sessions WHERE token = @token AND expiry > clock_timestamp() RETURNING signed_in_at;

-- name: LockLineageEnrolments :many
-- unscoped: a lineage is one browser's chain of renewed session tokens.
-- A logged-out lineage's authenticator enrolment is no one's, so another session may begin one.
-- A logout never waits for an account's lock: a row held by another change keeps its enrolment,
-- which BeginMFA lets go of once it finds the lineage signed out. The lock is the one an update
-- of the row takes, so a transaction that only references the account does not hold it off.
SELECT id FROM accounts WHERE totp_pending_lineage = @lineage::uuid FOR NO KEY UPDATE SKIP LOCKED;

-- name: EndEnrolments :exec
-- unscoped: the accounts are those LockLineageEnrolments locked for a logged-out lineage.
UPDATE accounts SET totp_pending = NULL,totp_pending_expiry = NULL,totp_pending_lineage = NULL
WHERE id = ANY(@ids::uuid[]);

-- name: DeleteSessionLineage :exec
-- unscoped: a lineage is one browser's chain of renewed session tokens.
DELETE FROM sessions WHERE lineage = @lineage;

-- name: MoveTroubleshootingBrowserBinding :exec
-- unscoped: the hashed browser session token identifies the sessions it opened.
UPDATE troubleshooting_sessions SET browser_session_hash = @renewed
WHERE browser_session_hash = @previous AND state <> 'closed';

-- name: BrowserSessionAdmission :one
-- unscoped: the hashed browser session token is the credential.
-- Revalidates an authenticated session under the lock its request waited for, and
-- returns its lineage.
-- Keep the validity predicate identical to BrowserSessionSnapshotAdmission.
SELECT s.lineage FROM accounts a JOIN sessions s ON s.token = @token AND s.expiry > clock_timestamp()
WHERE a.id = @account_id AND a.auth_version = @auth_version;

-- name: BrowserSessionSnapshotAdmission :one
-- unscoped: the hashed browser session token is the credential.
-- Fails to serialize if the account changed or the session was deleted after the
-- transaction's snapshot. KEY SHARE ignores a session's idle refresh.
-- Keep the validity predicate identical to BrowserSessionAdmission.
SELECT a.id FROM accounts a JOIN sessions s ON s.token = @token AND s.expiry > clock_timestamp()
WHERE a.id = @account_id AND a.auth_version = @auth_version
FOR SHARE OF a FOR KEY SHARE OF s;

-- name: SessionLineageLive :one
-- unscoped: a lineage is one browser's chain of renewed session tokens.
SELECT EXISTS(SELECT 1 FROM sessions WHERE lineage = @lineage AND expiry > clock_timestamp());

-- name: AccountSessions :many
-- unscoped: a browser session's account is the one it signed in to, which only that account reads.
-- The account's sessions that would be admitted, most recently seen first, each lineage once,
-- and how many there are in all. A row a credential change has moved on from is not one.
SELECT l.lineage,l.signed_in_at,l.last_seen_at,l.client_browser,l.client_os,l.client_network,count(*) OVER () AS live
FROM (
    SELECT DISTINCT ON (s.lineage) s.lineage,s.signed_in_at,s.last_seen_at,s.client_browser,s.client_os,s.client_network
    FROM sessions s JOIN accounts a ON a.id = s.account_id AND a.auth_version = s.auth_version
    WHERE s.account_id = @account_id::uuid AND s.expiry > clock_timestamp()
    ORDER BY s.lineage,s.last_seen_at DESC
) l
ORDER BY l.last_seen_at DESC,l.lineage
LIMIT @listed;

-- name: AccountSessionLineages :many
-- unscoped: a browser session's account is the one it signed in to, which only that account ends.
-- Every lineage holding a session of the account but the one named, including any a credential
-- change has moved on from but not yet renewed, so that renewal refuses.
SELECT DISTINCT lineage FROM sessions WHERE account_id = @account_id::uuid AND lineage <> @kept::uuid ORDER BY lineage;

-- name: AccountHasSessionLineage :one
-- unscoped: a browser session's account is the one it signed in to, which only that account ends.
SELECT EXISTS(SELECT 1 FROM sessions WHERE account_id = @account_id::uuid AND lineage = @lineage::uuid);
