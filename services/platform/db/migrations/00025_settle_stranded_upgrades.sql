-- +goose Up
-- A subscription lapse used to cancel a queued upgrade and leave its plan at
-- running, which blocks every other command on the server. That command never
-- reached the host, so its plan returns to the phase the host still holds. A
-- coordinated child is left alone: its parent holds stopped writers, and a phase
-- change here could not resume them.
UPDATE supported_upgrades u
SET status = u.status || jsonb_build_object(
    'phase', CASE o.command->'upgrade'->>'action' WHEN 'prepare' THEN 'rejected' WHEN 'cutover' THEN 'ready' ELSE 'attention' END,
    'note', 'This upgrade action stopped before reaching the host and changed nothing. The operation''s note says why.')
FROM (
    SELECT DISTINCT ON (workspace_id, command->>'resourceId') workspace_id, command, state, note, parent_id
    FROM execution_operations
    WHERE kind = 'upgrade'
    ORDER BY workspace_id, command->>'resourceId', created_at DESC
) o
WHERE o.workspace_id = u.workspace_id
    AND o.command->>'resourceId' = u.id::text
    AND o.parent_id IS NULL
    AND o.state = 'cancelled'
    AND o.note = 'Subscription expired before dispatch.'
    AND u.status->>'phase' = 'running';

-- A coordinated prepare that ended at its recovery checkpoint had no action left:
-- the parent is finished and nothing reached a host.
UPDATE supported_upgrades u
SET status = u.status || jsonb_build_object(
    'phase', 'rejected',
    'note', 'This upgrade action stopped before reaching the host and changed nothing. The operation''s note says why.')
FROM execution_operations o
WHERE o.workspace_id = u.workspace_id
    AND o.kind = 'crosshost'
    AND o.state IN ('cancelled','failed')
    AND o.parent_id IS NULL
    AND o.command->'crosshost'->'maintenance'->'effect'->'upgrade'->'plan'->>'id' = u.id::text
    AND o.note IN (
        'Cancelled before recovery metadata or any host effects were admitted.',
        'Recovery metadata exceeds the supported checkpoint limits; no host was reserved or changed.',
        'Cancelled before host reservation or writer maintenance began.',
        'The primary did not acknowledge recovery metadata; no host was reserved or paused.')
    AND u.status->>'phase' = 'running';

-- +goose Down
SELECT 1;
