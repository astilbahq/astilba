-- name: InsertAccountSecurityEvent :exec
-- unscoped: account security events belong to an account, not a workspace.
INSERT INTO account_security_events(id,account_id,kind,created_at) VALUES(@id,@account_id,@kind,@created_at);

-- name: AccountSecurityNotice :one
-- unscoped: account security events belong to an account, not a workspace.
SELECT e.kind,e.created_at,a.email FROM account_security_events e JOIN accounts a ON a.id = e.account_id
WHERE e.id = @id;
