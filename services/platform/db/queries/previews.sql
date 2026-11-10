-- name: DuePreviewPolicies :many
-- unscoped: the preview sweep spans workspaces. It takes every workspace's oldest due
-- policy before any workspace's second, so a backlog in one does not take another's slots.
-- The outer filter applies to the row as locked, which may have changed since the snapshot.
WITH due AS (
    SELECT id,next_discovery_at,row_number() OVER (PARTITION BY workspace_id ORDER BY next_discovery_at,id) AS turn
    FROM preview_policies WHERE enabled AND next_discovery_at<=now()
)
SELECT p.id,p.workspace_id FROM preview_policies p JOIN due ON due.id=p.id
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
SELECT r.id,r.workspace_id FROM preview_requests r JOIN due ON due.id=r.id
WHERE r.state<>'deleted' AND r.next_check_at<=now()
ORDER BY due.turn,due.next_check_at,due.id LIMIT 20 FOR UPDATE OF r SKIP LOCKED;

-- name: ReleaseRevokedHostPreviews :many
-- A policy runs every preview on its reserved host, and once that host is revoked nothing it says
-- can be accepted, so these could never be confirmed removed and would hold their slots for good.
-- Only allocated previews hold a slot. Management's runtime records stay as last known, but
-- ServerControllerInventory no longer expects them on the host.
UPDATE preview_requests r SET state='deleted',note = @note,released_by_revocation=true,cleanup_at=NULL,updated_at=now(),next_check_at=now()+interval '1 day'
FROM preview_policies p
WHERE p.workspace_id=r.workspace_id AND p.id=r.policy_id AND r.workspace_id = @workspace_id AND p.server_id = @server_id
    AND r.environment_id IS NOT NULL AND r.state<>'deleted'
RETURNING r.environment_id::uuid AS environment_id;

-- name: WithdrawRevokedHostPreviewApprovals :exec
-- An approved fork commit may be what compromised the host. Fork approvals are bound to the policy
-- generation, so advancing it means neither renewal nor a move runs one again without approval.
UPDATE preview_policies SET generation=generation+1,note = @note
WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: DelayPreviewDiscovery :exec
-- Discovery waits a cycle while its workspace has a backlog of request jobs.
UPDATE preview_policies SET next_discovery_at = now() + interval '5 minutes' WHERE workspace_id = @workspace_id AND id = @id;
