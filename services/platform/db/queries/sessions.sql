-- name: RefreshSession :execrows
-- unscoped: the hashed browser session token is the credential.
-- UPDATE serializes with logout, renewal or expiry cleanup and never recreates
-- a deleted token.
UPDATE sessions SET data = @data,expiry = @expiry WHERE token = @token;

-- name: InsertSession :exec
-- unscoped: the hashed browser session token is the credential.
INSERT INTO sessions(token,data,expiry) VALUES(@token,@data,@expiry);
