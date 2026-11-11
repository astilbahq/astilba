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
UPDATE network_members SET public_key = @public_key,note='',sweep_hold=NULL
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND enabled AND public_key='';

-- name: RefuseNetworkMemberKey :exec
-- Asked again, it would report the same key, so the sweep holds it until the network changes.
UPDATE network_members SET note = @note,sweep_hold='duplicate-key'
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND enabled AND public_key='';

-- name: AdvanceNetworkGeneration :one
-- Moves past @observed_generation, or by one when that is lower than the counter. At the bigint
-- limit it stays put rather than fail, because revoking a server runs in the same transaction
-- and must not depend on a counter a host can have moved.
UPDATE workspace_networks SET generation=CASE WHEN GREATEST(generation, @observed_generation::bigint)=9223372036854775807
    THEN 9223372036854775807 ELSE GREATEST(generation, @observed_generation::bigint)+1 END
WHERE workspace_id = @workspace_id RETURNING generation;

-- name: SetNetworkMemberGenerations :exec
-- A new generation is news for a member that was waiting on @unaccounted_note, that refused the
-- last one over @route_conflict_note, or that the sweep held, so it tries again. A removal can have
-- resolved a key another member held, and a held host is told about a revocation if it can apply it.
UPDATE network_members SET generation = @generation,sweep_hold=NULL,note=CASE WHEN note IN (@unaccounted_note, @route_conflict_note, @unprepared_note, @duplicate_key_note) THEN '' ELSE note END
WHERE workspace_id = @workspace_id;

-- name: MarkNetworkGenerationExhausted :exec
-- A generation that cannot advance leaves members looking acknowledged while they still hold
-- what was just removed, so every member goes back to pending and the enabled ones say why.
UPDATE network_members SET acknowledged_generation=0,note=CASE WHEN enabled THEN @note ELSE note END
WHERE workspace_id = @workspace_id;

-- name: HoldUnpreparedNetworkMember :exec
-- A host that could not run a command for this membership, while its heartbeat says it is not
-- prepared for managed networking. Otherwise the failure had another cause and the next pass tries
-- again. The note explains the hold only when it has nothing else to say.
UPDATE network_members m SET sweep_hold='unprepared',note=CASE WHEN m.note IN ('', @renewed_note) THEN @note ELSE m.note END
FROM servers s
WHERE m.workspace_id = @workspace_id AND m.server_id = @server_id AND m.membership_id = @membership_id
    AND s.id = m.server_id AND NOT COALESCE((s.capabilities->>'crossHost')::boolean,false);

-- name: SetNetworkMemberNote :exec
UPDATE network_members SET note = @note WHERE workspace_id = @workspace_id AND server_id = @server_id;

-- name: NetworkManifestLanding :one
-- A whole manifest at this generation already carries any removal, and a removal sent alongside it
-- would reach the host first and make the host refuse it. One that has not landed within this long
-- is held up, so a removal goes without it.
SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id = @workspace_id AND server_id = @server_id AND kind='network'
    AND command->'network'->>'action'='configure'
    AND (command->'network'->'manifest'->>'generation')::bigint = @generation::bigint
    AND state NOT IN ('succeeded','failed','cancelled') AND created_at > now()-interval '30 seconds');

-- name: NetworkManifestMissed :one
-- A whole manifest at this generation that ended without landing, for instance because the host had
-- already taken a later command, carried no removal there. Another may not land either, so the
-- removal goes first, on its own.
SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id = @workspace_id AND server_id = @server_id AND kind='network'
    AND command->'network'->>'action'='configure'
    AND command->'network'->>'membershipId' = @membership_id::text
    AND (command->'network'->'manifest'->>'generation')::bigint = @generation::bigint
    AND state IN ('failed','cancelled'));

-- name: NoteRefusedNetworkConfigure :exec
-- Only for the member's latest command: anything newer has not been refused.
UPDATE network_members SET note = @note
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND generation = @generation;

-- name: NetworkGrantsIssuedTo :one
-- Whether every peer and grant in a manifest a host says it holds is one this management made for
-- the workspace's network and this host. Members, endpoints and their sources are never deleted, so
-- all it sent is still here unless management was restored from an older backup. A peer's
-- membership and key, and a source's ingress, may since have changed.
WITH m AS (SELECT @manifest::jsonb AS doc),
peers AS (SELECT jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'peers')='array' THEN doc->'peers' ELSE '[]' END) AS p FROM m),
exports AS (SELECT jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'exports')='array' THEN doc->'exports' ELSE '[]' END) AS x FROM m),
sources AS (SELECT x, jsonb_array_elements(CASE WHEN jsonb_typeof(x->'sources')='array' THEN x->'sources' ELSE '[]' END) AS s FROM exports),
egress AS (SELECT jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'egress')='array' THEN doc->'egress' ELSE '[]' END) AS g FROM m),
unissued AS (
    SELECT 1 FROM peers WHERE NOT EXISTS(SELECT 1 FROM network_members n WHERE n.workspace_id = @workspace_id AND n.server_id = (p->>'serverId')::uuid)
    UNION ALL
    SELECT 1 FROM exports WHERE NOT EXISTS(SELECT 1 FROM network_endpoints e WHERE e.workspace_id = @workspace_id AND e.id = (x->>'id')::uuid AND e.server_id = @server_id
        AND e.unit_id = (x->>'unitId')::uuid AND e.resource_id = (x->>'resourceId')::uuid AND e.instance_id = (x->>'instanceId')::uuid AND e.listen_port = (x->>'port')::integer AND e.container_port = (x->>'containerPort')::integer)
    UNION ALL
    SELECT 1 FROM sources WHERE NOT EXISTS(SELECT 1 FROM network_endpoint_sources r WHERE r.workspace_id = @workspace_id AND r.endpoint_id = (x->>'id')::uuid AND r.server_id = (s->>'serverId')::uuid AND r.unit_id = (s->>'unitId')::uuid)
    UNION ALL
    SELECT 1 FROM egress WHERE NOT EXISTS(SELECT 1 FROM network_endpoint_sources r JOIN network_endpoints e ON e.workspace_id = r.workspace_id AND e.id = r.endpoint_id
        WHERE r.workspace_id = @workspace_id AND e.id = (g->>'endpointId')::uuid AND e.server_id = (g->>'serverId')::uuid AND e.listen_port = (g->>'port')::integer AND r.server_id = @server_id AND r.unit_id = (g->>'unitId')::uuid)
)
SELECT NOT EXISTS(SELECT 1 FROM unissued) AS issued;

-- name: MemberInstalledManifest :one
SELECT installed_manifest FROM network_members
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id FOR UPDATE;

-- name: RecordRefusingNetworkMember :exec
-- A host that refused a configure over a route conflict says what it holds, so removals can be
-- scoped from it although management had no record. Only for the member's latest command, as
-- with its note.
UPDATE network_members SET installed_manifest = @installed_manifest
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id AND generation = @generation;

-- name: ForgetInstalledNetwork :exec
-- The host refused a removal built from this record, so the record does not describe the host.
UPDATE network_members SET installed_manifest=NULL,manifest_hash=''
WHERE workspace_id = @workspace_id AND server_id = @server_id AND membership_id = @membership_id;

-- name: DisableNetworkMember :execrows
UPDATE network_members SET enabled=false,note = @note
WHERE workspace_id = @workspace_id AND server_id = @server_id AND enabled;

-- name: ReportsOnNetworkCommand :one
-- A command's kind never changes, so this is read before the report's transaction to choose its
-- locks. Reading nothing leaves the report to be refused as it would be anyway.
SELECT EXISTS(SELECT 1 FROM execution_operations WHERE id = @id AND server_id = @server_id AND kind='network');
