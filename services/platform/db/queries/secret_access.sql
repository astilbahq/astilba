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
-- was down for a day, and scanning all history every minute would not finish; a record it
-- missed has no outcome, which reads as unknown. An operator's rotation has no time limit, so it
-- is never presumed abandoned.
INSERT INTO secret_access_outcomes(access_id,outcome)
SELECT a.id,'abandoned' FROM secret_access a
WHERE a.purpose <> 'rotate'
    AND a.at < now() - make_interval(secs => @older_than_seconds::double precision)
    AND a.at > now() - interval '1 day'
    AND NOT EXISTS(SELECT 1 FROM secret_access_outcomes o WHERE o.access_id = a.id)
ORDER BY a.at LIMIT 1000;

-- name: SecretAccessWorkspaceExists :one
SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = @id)::boolean;

-- name: SecretRelease :one
-- The lease is timed by the database clock, as the claim's is, so the two agree.
SELECT digest,attempts,last_attempt_at,acknowledged_at,redelivery,now()::timestamptz AS checked_at FROM secret_releases
WHERE workspace_id = @workspace_id AND operation_id = @operation_id AND dispatch_epoch = @dispatch_epoch AND agent_instance = @agent_instance;

-- name: LockSecretRelease :one
SELECT digest,attempts,last_attempt_at,acknowledged_at,redelivery,now()::timestamptz AS checked_at FROM secret_releases
WHERE workspace_id = @workspace_id AND operation_id = @operation_id AND dispatch_epoch = @dispatch_epoch AND agent_instance = @agent_instance FOR UPDATE;

-- name: InsertSecretRelease :execrows
INSERT INTO secret_releases(operation_id,dispatch_epoch,agent_instance,workspace_id,server_id,digest,attempts,last_attempt_at)
VALUES(@operation_id,@dispatch_epoch,@agent_instance,@workspace_id,@server_id,@digest,1,now())
ON CONFLICT DO NOTHING;

-- name: RepeatSecretRelease :exec
UPDATE secret_releases SET digest = @digest,attempts = attempts + 1,last_attempt_at = now(),acknowledged_at = NULL,redelivery = false
WHERE workspace_id = @workspace_id AND operation_id = @operation_id AND dispatch_epoch = @dispatch_epoch AND agent_instance = @agent_instance;

-- name: AcknowledgeSecretRelease :execrows
UPDATE secret_releases SET acknowledged_at = COALESCE(acknowledged_at, now())
WHERE workspace_id = @workspace_id AND operation_id = @operation_id AND dispatch_epoch = @dispatch_epoch
    AND agent_instance = @agent_instance AND digest = @digest;

-- name: RequestSecretRedelivery :execrows
-- The instance does not hold what was released to it: the next poll releases it again, as a
-- re-delivery. A request is honoured past the attempt limit, which bounds only the releases
-- the instance never answered.
UPDATE secret_releases SET acknowledged_at = NULL,redelivery = true,last_attempt_at = now() - interval '1 day'
WHERE workspace_id = @workspace_id AND operation_id = @operation_id AND dispatch_epoch = @dispatch_epoch
    AND agent_instance = @agent_instance;

-- name: AddOperationNote :exec
-- A release that cannot go says why beside whatever the operation already says, such as a
-- stopped submission's warning, and only once.
UPDATE execution_operations SET note = CASE WHEN note = '' THEN @note::text ELSE note || ' ' || @note::text END
WHERE workspace_id = @workspace_id AND id = @id AND strpos(note, @note::text) = 0;
