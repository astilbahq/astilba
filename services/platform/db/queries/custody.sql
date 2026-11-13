-- Custody: the installation's identity, its key backends and canaries, and its key events.

-- name: InstallationID :one
SELECT id FROM installation;

-- name: LockCustody :exec
-- Serializes changes to the installation's backends and canaries between instances.
SELECT pg_advisory_xact_lock(4281642305);

-- name: CustodyBackends :many
SELECT backend_id,role FROM custody_backends ORDER BY created_at,backend_id;

-- name: RegisterCustodyBackend :exec
INSERT INTO custody_backends(backend_id,role) VALUES(@backend_id,'read+write');

-- name: InstallationKeyCanaries :many
SELECT class,wrapped FROM installation_key_canaries WHERE backend_id = @backend_id ORDER BY class;

-- name: InsertInstallationKeyCanary :execrows
INSERT INTO installation_key_canaries(backend_id,class,wrapped) VALUES(@backend_id,@class,@wrapped)
ON CONFLICT (backend_id,class) DO NOTHING;

-- name: RewrapInstallationKeyCanary :execrows
-- Replaces the wrapping it was read as, so a concurrent rewrap's is never overwritten.
UPDATE installation_key_canaries SET wrapped = @wrapped
WHERE backend_id = @backend_id AND class = @class AND wrapped = @previous;

-- name: InsertInstallationKeyEvent :exec
INSERT INTO installation_key_events(id,run_id,kind,operator,reason,backend_id,scope,workspace_id,generation,details)
VALUES(@id,@run_id,@kind,sqlc.narg(operator),sqlc.narg(reason),sqlc.narg(backend_id),sqlc.narg(scope),sqlc.narg(workspace_id),sqlc.narg(generation),@details);

-- name: InstallationKeyEvents :many
SELECT id,at,run_id,kind,operator,reason,backend_id,scope,workspace_id,generation,details FROM installation_key_events
ORDER BY at DESC,id DESC LIMIT @page_size;

-- name: UnfinishedCustodyRuns :many
-- A command records `<name>-started` first and `<name>-finished` last, so a start with no finish is
-- one that stopped part way, or is running now.
SELECT s.run_id,s.kind,s.at,s.operator FROM installation_key_events s
WHERE s.kind LIKE '%-started'
    AND NOT EXISTS(SELECT 1 FROM installation_key_events f WHERE f.run_id = s.run_id AND f.kind LIKE '%-finished')
ORDER BY s.at;

-- name: CustodyStage :one
SELECT stage FROM custody_stage;
