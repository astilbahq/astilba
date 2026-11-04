-- name: RefreshSession :execrows
-- unscoped: the hashed browser session token is the credential.
-- UPDATE serializes with logout, renewal or expiry cleanup and never recreates
-- a deleted token.
UPDATE sessions SET data = @data,expiry = @expiry WHERE token = @token;

-- name: InsertSession :exec
-- unscoped: the hashed browser session token is the credential.
INSERT INTO sessions(token,data,expiry) VALUES(@token,@data,@expiry);

-- name: DeleteRenewedSession :execrows
-- unscoped: the hashed browser session token is the credential.
DELETE FROM sessions WHERE token = @token;

-- name: MoveTroubleshootingBrowserBinding :exec
-- unscoped: the hashed browser session token identifies the sessions it opened.
UPDATE troubleshooting_sessions SET browser_session_hash = @renewed
WHERE browser_session_hash = @previous AND state <> 'closed';
