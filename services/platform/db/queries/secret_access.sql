-- name: SecretAccessMember :one
SELECT role FROM memberships WHERE workspace_id = @workspace_id AND account_id = @account_id;

-- name: SecretAccessServer :one
SELECT EXISTS(SELECT 1 FROM servers WHERE workspace_id = @workspace_id AND id = @server_id AND NOT revoked)::boolean;

-- name: InsertSecretAccess :exec
INSERT INTO secret_access(id,workspace_id,purpose,principal_kind,account_id,automation_id,server_id,agent_instance,job,operator_ref,operation_id,items,item_count,generations,backend_id,reason)
VALUES(@id,sqlc.narg(workspace_id),@purpose,@principal_kind,sqlc.narg(account_id),sqlc.narg(automation_id),sqlc.narg(server_id),sqlc.narg(agent_instance),sqlc.narg(job),sqlc.narg(operator_ref),sqlc.narg(operation_id),@items,@item_count,@generations,@backend_id,@reason);

-- name: InsertSecretAccessOutcome :exec
INSERT INTO secret_access_outcomes(access_id,outcome,provider_request_ids) VALUES(@access_id,@outcome,@provider_request_ids);

-- name: AbandonSecretAccess :execrows
-- A session never outlives its cap, so a record older than that with no outcome is one whose
-- process stopped before closing it. Each run marks a bounded batch, and looks back only a day:
-- the sweep runs every minute, so anything older was either marked or missed while management
-- was down for a day, and scanning all history every minute would not finish.
INSERT INTO secret_access_outcomes(access_id,outcome)
SELECT a.id,'abandoned' FROM secret_access a
WHERE a.at < now() - make_interval(secs => @older_than_seconds::double precision)
    AND a.at > now() - interval '1 day'
    AND NOT EXISTS(SELECT 1 FROM secret_access_outcomes o WHERE o.access_id = a.id)
ORDER BY a.at LIMIT 1000;

-- name: SecretAccessWorkspaceExists :one
SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = @id)::boolean;
