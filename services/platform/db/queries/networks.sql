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

-- name: AdvanceNetworkGeneration :one
-- Moves past @observed_generation, or by one when that is lower than the counter. At the bigint
-- limit it stays put rather than fail, because revoking a server runs in the same transaction
-- and must not depend on a counter a host can have moved.
UPDATE workspace_networks SET generation=CASE WHEN GREATEST(generation, @observed_generation::bigint)=9223372036854775807
    THEN 9223372036854775807 ELSE GREATEST(generation, @observed_generation::bigint)+1 END
WHERE workspace_id = @workspace_id RETURNING generation;

-- name: SetNetworkMemberGenerations :exec
-- A new generation is news for a member that was waiting on @unaccounted_note, so it tries again.
UPDATE network_members SET generation = @generation,note=CASE WHEN note = @unaccounted_note THEN '' ELSE note END
WHERE workspace_id = @workspace_id;

-- name: MarkNetworkGenerationExhausted :exec
-- A generation that cannot advance leaves members looking acknowledged while they still hold
-- what was just removed, so every member goes back to pending and the enabled ones say why.
UPDATE network_members SET acknowledged_generation=0,note=CASE WHEN enabled THEN @note ELSE note END
WHERE workspace_id = @workspace_id;

-- name: SetNetworkMemberNote :exec
UPDATE network_members SET note = @note WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: RecordNetworkHistoryClaim :exec
UPDATE network_members SET history_claim_controller = @controller
WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: DisableNetworkMember :execrows
UPDATE network_members SET enabled=false,note='Server authority revoked; peer removals await acknowledgement.'
WHERE workspace_id = @workspace_id AND server_id = @server_id AND enabled;
