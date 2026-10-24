-- name: InsertHostedSubscription :exec
INSERT INTO hosted_subscriptions(workspace_id,server_limit) VALUES($1,$2);

-- name: MarkEmailVerified :exec
UPDATE accounts SET email_verified_at=$2 WHERE id=$1;
