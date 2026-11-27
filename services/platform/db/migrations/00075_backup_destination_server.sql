-- +goose Up
-- astilba: not additive: it binds existing destinations in a backfill and adds a check an older release's writes don't know.
-- The server an S3 destination's storage keys are given to. Only policies on that server use the
-- destination, so its keys, which the owner limits to its prefix or bucket, reach no other
-- server's repositories. A new S3 destination is bound when it is created, and one without a
-- server yet is bound by its first policy. A destination already used by policies on more than one
-- server is shared: those policies keep using it, and no new policy can. A filesystem destination
-- is a directory on each server and holds no keys, so it is never bound. See
-- docs/backups.md#destination-setup.
ALTER TABLE backup_destinations ADD COLUMN server_id uuid,
 ADD COLUMN shared boolean NOT NULL DEFAULT false,
 ADD FOREIGN KEY(workspace_id,server_id) REFERENCES servers(workspace_id,id);

UPDATE backup_destinations d SET server_id = u.server_id, shared = u.servers > 1
FROM (SELECT p.workspace_id, p.configuration->>'destinationId' AS destination_id, min(p.server_id::text)::uuid AS server_id, count(DISTINCT p.server_id) AS servers
    FROM backup_policies p WHERE p.configuration->>'method' <> 'container-job'
    GROUP BY p.workspace_id, p.configuration->>'destinationId') u
WHERE d.workspace_id = u.workspace_id AND d.id::text = u.destination_id AND d.configuration->>'kind' = 's3';
UPDATE backup_destinations SET server_id = NULL WHERE shared;

ALTER TABLE backup_destinations ADD CONSTRAINT backup_destination_binding
 CHECK (NOT (shared AND server_id IS NOT NULL) AND (configuration->>'kind' = 's3' OR (server_id IS NULL AND NOT shared)));

-- +goose Down
ALTER TABLE backup_destinations DROP CONSTRAINT backup_destination_binding;
ALTER TABLE backup_destinations DROP COLUMN shared, DROP COLUMN server_id;
