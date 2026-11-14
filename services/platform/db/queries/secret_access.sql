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

-- name: ListSecretAccess :many
-- A workspace's records, newest first, a page at a time, with who they were for and how each
-- ended: the first outcome written, or unknown while a session is open or its end went
-- unrecorded. An account is named only while it is a member: one that left keeps its ID.
SELECT a.id,a.at,a.purpose,a.principal_kind,a.account_id,acc.email AS account_email,a.automation_id,cred.name AS automation_name,
    a.server_id,srv.name AS server_name,a.agent_instance,a.job,a.operation_id,a.items,a.item_count,a.reason,
    COALESCE(o.outcome,'unknown')::text AS outcome
FROM secret_access a
LEFT JOIN memberships m ON m.workspace_id = a.workspace_id AND m.account_id = a.account_id
LEFT JOIN accounts acc ON acc.id = m.account_id
LEFT JOIN automation_credentials cred ON cred.workspace_id = a.workspace_id AND cred.id = a.automation_id
LEFT JOIN servers srv ON srv.workspace_id = a.workspace_id AND srv.id = a.server_id
LEFT JOIN LATERAL (SELECT outcome FROM secret_access_outcomes WHERE access_id = a.id ORDER BY at LIMIT 1) o ON true
WHERE a.workspace_id = @workspace_id
    AND (sqlc.narg(before)::uuid IS NULL OR (a.at,a.id) < (SELECT b.at,b.id FROM secret_access b WHERE b.workspace_id = @workspace_id AND b.id = sqlc.narg(before)::uuid))
ORDER BY a.at DESC,a.id DESC LIMIT @page_size;

-- name: SecretAccessItemNames :many
-- Names for the items a page of records names, where they still exist.
SELECT 'secret-version'::text AS kind,v.id,s.name FROM secret_versions v JOIN secrets s ON s.workspace_id = v.workspace_id AND s.id = v.secret_id
    WHERE v.workspace_id = @workspace_id AND v.id = ANY(@ids::uuid[])
UNION ALL SELECT 'build-secret',id,name FROM build_secret_versions WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[])
UNION ALL SELECT 'git',id,COALESCE(configuration->>'name','') FROM git_connections WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[])
UNION ALL SELECT 'registry',id,COALESCE(configuration->>'name','') FROM registries WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[])
UNION ALL SELECT 'backup-destination',id,COALESCE(configuration->>'name','') FROM backup_destinations WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[])
UNION ALL SELECT 'backup-policy',id,COALESCE(configuration->>'name','') FROM backup_policies WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]);

-- name: ExpireSecretAccess :one
-- unscoped: retention applies to every workspace's records alike.
SELECT expire_secret_access(@batch::integer)::integer AS expired;

-- name: SetSecretAccessRetention :exec
-- unscoped: one installation-wide setting, written only by `astilba migrate`.
UPDATE secret_access_settings SET retention_days = @days::integer;
