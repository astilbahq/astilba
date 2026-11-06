-- name: DuePreviewPolicies :many
-- unscoped: the preview sweep spans workspaces. It takes every workspace's oldest due
-- policy before any workspace's second, so a backlog in one does not take another's slots.
-- The outer filter applies to the row as locked, which may have changed since the snapshot.
WITH due AS (
    SELECT id,next_discovery_at,row_number() OVER (PARTITION BY workspace_id ORDER BY next_discovery_at,id) AS turn
    FROM preview_policies WHERE enabled AND next_discovery_at<=now()
)
SELECT p.id FROM preview_policies p JOIN due ON due.id=p.id
WHERE p.enabled AND p.next_discovery_at<=now()
ORDER BY due.turn,due.next_discovery_at,due.id LIMIT 10 FOR UPDATE OF p SKIP LOCKED;

-- name: DuePreviewRequests :many
-- unscoped: the preview sweep spans workspaces. It takes every workspace's oldest due
-- request before any workspace's second, so a backlog in one does not take another's slots.
-- The outer filter applies to the row as locked, which may have changed since the snapshot.
WITH due AS (
    SELECT id,next_check_at,row_number() OVER (PARTITION BY workspace_id ORDER BY next_check_at,id) AS turn
    FROM preview_requests WHERE state<>'deleted' AND next_check_at<=now()
)
SELECT r.id FROM preview_requests r JOIN due ON due.id=r.id
WHERE r.state<>'deleted' AND r.next_check_at<=now()
ORDER BY due.turn,due.next_check_at,due.id LIMIT 20 FOR UPDATE OF r SKIP LOCKED;
