-- name: SweepPassStarted :one
-- The database's clock, because the scan cursors are written with it.
SELECT clock_timestamp()::timestamptz;

-- name: ClaimNetworkForSweep :one
-- unscoped: the network sweep visits every workspace, moving each to the back of the scan
-- before handling it, so a workspace that fails cannot hold the head of the scan.
-- pass_started gives each network one turn per pass.
UPDATE workspace_networks SET checked_at=clock_timestamp()
WHERE id=(SELECT n.id FROM workspace_networks n WHERE n.checked_at < @pass_started::timestamptz
    ORDER BY n.checked_at,n.workspace_id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING workspace_id;

-- name: RecordNetworkSweepOutcome :exec
-- Keeps the start of a run of failures, which is what the alert judges. A row another
-- transaction holds is skipped so a tenant cannot make the sweep wait here; the next
-- pass records the outcome instead.
UPDATE workspace_networks SET reconcile_failing_since=CASE WHEN @failed::boolean THEN COALESCE(reconcile_failing_since,clock_timestamp()) END
WHERE id=(SELECT n.id FROM workspace_networks n WHERE n.workspace_id = @workspace_id AND (@failed::boolean OR n.reconcile_failing_since IS NOT NULL) FOR UPDATE SKIP LOCKED);

-- name: NetworkKeyHeldByAnotherMember :one
-- Disabled members count: their pending removal names the key on the peers that still hold it.
SELECT EXISTS(SELECT 1 FROM network_members
    WHERE workspace_id = @workspace_id AND network_id = @network_id AND public_key = @public_key AND membership_id <> @membership_id);

-- name: RecordNetworkMemberKey :exec
UPDATE network_members SET public_key = @public_key,note=''
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND enabled AND public_key='';

-- name: RefuseNetworkMemberKey :exec
UPDATE network_members SET note = @note
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND enabled AND public_key='';
