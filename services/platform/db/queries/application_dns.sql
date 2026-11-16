-- Application DNS publications: where this installation may have provider records,
-- and the audit of every record it deleted. See docs/hosting.md#generated-application-names.

-- name: ListDNSPublications :many
-- unscoped: a DNS job names the server; each row carries the workspace it was recorded for.
SELECT workspace_id,base,zone,revoked FROM dns_publications WHERE server_id = @server_id ORDER BY base,zone;

-- name: RevokeDNSPublications :execrows
-- unscoped: revocation already established the server's workspace.
UPDATE dns_publications SET revoked=true,checked_at=NULL WHERE server_id = @server_id;

-- name: RecordDNSPublication :exec
-- Written and committed before the provider is called, so a lost response still leaves a row.
-- A row from before zones were recorded names the same records, so it is replaced. Publishing
-- needs an owner's approval given after any revocation, so it ends the revocation's withdrawal.
WITH legacy AS (
    DELETE FROM dns_publications WHERE server_id = @server_id AND base = @base AND zone = '' AND @zone::text <> ''
)
INSERT INTO dns_publications(server_id,workspace_id,base,zone) VALUES(@server_id,@workspace_id,@base,@zone)
ON CONFLICT(server_id,base,zone) DO UPDATE SET revoked=false;

-- name: CheckDNSPublication :exec
UPDATE dns_publications SET checked_at=clock_timestamp()
WHERE server_id = @server_id AND base = @base AND zone = @zone;

-- name: DeleteDNSPublication :exec
DELETE FROM dns_publications WHERE server_id = @server_id AND base = @base AND zone = @zone;

-- name: InsertDNSRecordRemoval :exec
-- Written before the deletion is sent. An unconfirmed row is from an attempt that may have
-- failed, and the record may since have been published again, so the latest attempt replaces it.
INSERT INTO dns_record_removals(workspace_id,server_id,name,zone,record_id,record_type,content,reason)
VALUES(@workspace_id,@server_id,@name,@zone,@record_id,@record_type,@content,@reason)
ON CONFLICT(zone,record_id) DO UPDATE SET content=EXCLUDED.content,reason=EXCLUDED.reason,requested_at=EXCLUDED.requested_at,hold_id=NULL
WHERE dns_record_removals.confirmed_at IS NULL;

-- name: ConfirmDNSRecordRemovals :exec
-- unscoped: a DNS job names the server. Called once the provider lists none of these records
-- except those kept, which are still published.
UPDATE dns_record_removals SET confirmed_at=clock_timestamp()
WHERE server_id = @server_id AND name = @name AND zone = @zone AND confirmed_at IS NULL AND NOT (record_id = ANY(@kept::text[]));

-- name: NoteWithdrawnServerDomain :exec
-- unscoped: a DNS job names the server, which establishes the workspace.
UPDATE server_domains SET records='{}',observed_at=NULL,note = @note,
    state=CASE WHEN state IN ('manual','review_required') THEN state ELSE 'pending' END
WHERE server_id = @server_id AND base = @base;

-- name: WithdrawRevokedServerDomain :one
-- Renewal may bring the server back on another machine or address, so an owner approves again.
UPDATE server_domains SET approved_controller=NULL,generation=generation+1,state='review_required',note = @note,observed_at=NULL
WHERE workspace_id = @workspace_id AND server_id = @server_id
RETURNING generation;

-- name: MarkRevokedServersDNSPublications :exec
-- unscoped: the sweep serves every workspace. It catches a revocation that reached servers
-- without passing through withdrawal, such as offline recovery merging a newer export.
UPDATE dns_publications p SET revoked=true,checked_at=NULL
FROM servers s WHERE s.id=p.server_id AND s.revoked AND NOT p.revoked;

-- name: StaleDNSPublicationServers :many
-- unscoped: the sweep serves every workspace. Rows in another zone are stranded and left out. A
-- due late name is picked up in any zone: the job records it, and a stranded one is then counted.
SELECT x.server_id,COALESCE(max(d.generation),0)::bigint AS generation,COALESCE(max(d.approved_controller),0)::bigint AS controller
FROM (
    SELECT p.server_id,COALESCE(p.checked_at,'-infinity'::timestamptz) AS since FROM dns_publications p
    WHERE p.zone IN ('',@zone::text) AND (p.checked_at IS NULL OR p.checked_at<now()-interval '1 minute')
        AND (p.revoked OR p.base <> @base::text
            OR (@hosted::boolean AND NOT EXISTS (SELECT 1 FROM hosted_subscriptions h WHERE h.workspace_id=p.workspace_id AND h.management_until>now())))
    UNION ALL
    SELECT l.server_id,l.due_at FROM dns_late_publications l WHERE l.due_at<=now()
) x
LEFT JOIN server_domains d ON d.server_id=x.server_id
GROUP BY x.server_id
ORDER BY min(x.since),x.server_id
LIMIT 100;

-- name: CountStrandedDNSPublications :one
-- unscoped: an installation-wide startup check.
-- A late name not yet recorded as a publication counts too, once.
SELECT count(*)::bigint AS count FROM (
    SELECT server_id,base,zone FROM dns_publications
    UNION
    SELECT server_id,base,zone FROM dns_late_publications
) n WHERE NOT @automatic::boolean OR n.zone NOT IN ('',@zone::text);

-- name: TakeDNSLease :execrows
-- unscoped: a DNS job names the server. The lease is taken when it is free or has run out.
INSERT INTO server_dns_leases(server_id,holder,expires_at) VALUES(@server_id,@holder,clock_timestamp()+make_interval(secs => @seconds::float8))
ON CONFLICT(server_id) DO UPDATE SET holder=EXCLUDED.holder,expires_at=EXCLUDED.expires_at
WHERE server_dns_leases.expires_at<=clock_timestamp();

-- name: HoldDNSLease :execrows
-- unscoped: a DNS job names the server. Locks the lease for the rest of its holder's
-- transaction, so a job taking it over waits until that commits.
SELECT 1 FROM server_dns_leases WHERE server_id = @server_id AND holder = @holder FOR UPDATE;

-- name: ReleaseDNSLease :exec
-- unscoped: a DNS job names the server.
DELETE FROM server_dns_leases WHERE server_id = @server_id AND holder = @holder;

-- name: DNSPublicationTarget :one
-- unscoped: a DNS job names the server. The addresses a job may publish: the generation it was
-- queued for, still approved under the server's current controller, in the configured base.
-- With them, when they were approved and the records the last job observed, and in which zone,
-- which the job judges the provider's records against.
SELECT d.workspace_id,d.base,d.ipv4,d.ipv6,d.approved_at,d.records,d.observed_zone,i.id AS installation_id FROM server_domains d JOIN servers s ON s.id=d.server_id CROSS JOIN installation i
WHERE d.server_id = @server_id AND d.generation = @generation AND d.approved_controller = @controller AND s.controller_generation = @controller
    AND NOT s.revoked AND NOT s.awaiting_transfer AND d.base = @base;

-- name: AuditedDNSRecordRemovals :many
-- unscoped: a DNS job names the records. Which of these records in the zone this database has
-- audited a deletion of, and so knows why they are gone.
SELECT record_id FROM dns_record_removals WHERE zone = @zone AND record_id = ANY(@record_ids::text[]);

-- name: EndOutdatedServerDomain :execrows
-- unscoped: a DNS job or the zone sweep names the server, holding its DNS lock. Ends an approval
-- the provider's zone shows is older than its records, as long as it is the approval that was
-- judged, so an owner approves the addresses again.
UPDATE server_domains SET approved_controller=NULL,generation=generation+1,state='review_required',note = @note,observed_at=NULL
WHERE server_id = @server_id AND generation = @generation AND approved_controller = @controller;

-- name: ObserveServerDomain :exec
-- unscoped: a DNS job names the server. Ignored once the profile has moved on.
UPDATE server_domains SET state = @state,note = @note,observed_at=clock_timestamp(),records = @records,observed_zone = @zone
WHERE server_id = @server_id AND generation = @generation AND approved_controller = @controller;

-- name: RecordLateDNSPublication :exec
-- unscoped: a DNS job names the server. Written before anything is queued, so a job's arguments
-- are never the only record of a name it may have published. due_at keeps any job from
-- recording it before a request that got no answer has had the margin to land.
INSERT INTO dns_late_publications(server_id,base,zone,generation,due_at)
SELECT s.id,@base,@zone,@generation,clock_timestamp()+make_interval(secs => @delay::float8) FROM servers s WHERE s.id = @server_id
ON CONFLICT(server_id,base,zone) DO UPDATE SET generation=LEAST(dns_late_publications.generation,EXCLUDED.generation),
    due_at=GREATEST(dns_late_publications.due_at,EXCLUDED.due_at);

-- name: AdoptLateDNSPublications :one
-- unscoped: a DNS job names the server, holding its lease. Each due late name becomes a
-- publication, kept for withdrawal when the server is revoked or the approval the late job
-- published under has since ended, as revocation does; a recovery fence ends approval without
-- a new generation, and keeps records as before. A publication already there keeps its state.
WITH due AS (
    DELETE FROM dns_late_publications l WHERE l.server_id = @server_id AND l.due_at<=clock_timestamp()
    RETURNING l.base,l.zone,l.generation
), adopted AS (
    INSERT INTO dns_publications(server_id,workspace_id,base,zone,revoked)
    SELECT s.id,s.workspace_id,due.base,due.zone,s.revoked OR COALESCE(d.approved_controller IS NULL AND d.generation <> due.generation,true)
    FROM due JOIN servers s ON s.id = @server_id LEFT JOIN server_domains d ON d.server_id=s.id
    ON CONFLICT(server_id,base,zone) DO NOTHING
)
SELECT count(*)::bigint FROM due;

-- name: CurrentDNSJob :one
-- unscoped: a DNS job names the server. The arguments of a job for the server's names as they
-- are now, as revocation and the sweep queue it.
SELECT COALESCE(max(generation),0)::bigint AS generation,COALESCE(max(approved_controller),0)::bigint AS controller
FROM server_domains WHERE server_id = @server_id;

-- name: ZoneRecordBacking :many
-- unscoped: the zone sweep serves every workspace. For each server and base the provider's zone
-- holds this installation's records under, in the order given: whether this database knows the server, its workspace,
-- whether a publication or a late name records the name, and whether the server's address approval
-- is current for that base, the configured one. The approval's arguments are those of its DNS job.
WITH given AS (
    SELECT @zone::text AS zone,@configured::text AS configured
), f AS (
    SELECT a.i,a.server_id,b.base FROM unnest(@server_ids::uuid[]) WITH ORDINALITY AS a(server_id,i)
    JOIN unnest(@bases::text[]) WITH ORDINALITY AS b(base,i) ON b.i=a.i
)
SELECT f.server_id::uuid AS server_id,f.base::text AS base,(s.id IS NOT NULL)::boolean AS known,s.workspace_id,
    (EXISTS(SELECT 1 FROM dns_publications p WHERE p.server_id=f.server_id AND p.base=f.base AND p.zone IN ('',given.zone))
        OR EXISTS(SELECT 1 FROM dns_late_publications l WHERE l.server_id=f.server_id AND l.base=f.base AND l.zone=given.zone))::boolean AS recorded,
    COALESCE(d.base=f.base AND f.base=given.configured AND d.approved_controller=s.controller_generation AND NOT s.revoked AND NOT s.awaiting_transfer,false)::boolean AS approved,
    COALESCE(d.generation,0)::bigint AS generation,COALESCE(d.approved_controller,0)::bigint AS controller,
    -- The server's last approval, when it was for this base, which records there are judged against.
    COALESCE(d.base=f.base AND NOT s.revoked,false)::boolean AS approval_here,
    COALESCE(d.ipv4,'')::text AS ipv4,COALESCE(d.ipv6,'')::text AS ipv6,d.approved_at
FROM f CROSS JOIN given
LEFT JOIN servers s ON s.id=f.server_id
LEFT JOIN server_domains d ON d.server_id=f.server_id
ORDER BY f.i;

-- name: AdoptZonePublication :execrows
-- unscoped: the zone sweep names the server, holding its DNS lock. Records a name the provider
-- holds our records under, which the database lost, while the server's approval for it is current.
-- An existing publication keeps its state: a revocation's withdrawal is never undone here.
INSERT INTO dns_publications(server_id,workspace_id,base,zone)
SELECT s.id,s.workspace_id,d.base,@zone FROM servers s JOIN server_domains d ON d.server_id=s.id
WHERE s.id = @server_id AND d.base = @base AND d.base = @configured::text AND d.approved_controller=s.controller_generation
    AND NOT s.revoked AND NOT s.awaiting_transfer
ON CONFLICT(server_id,base,zone) DO NOTHING;

-- name: InsertZoneDNSRecordRemoval :execrows
-- unscoped: the zone sweep names the server. Written before the deletion is sent, for a record
-- nothing backs, or one changed after its server's approval. The workspace is the server's, or
-- none for a server this database has never heard of; the row is written only while the server is
-- as known or unknown as the sweep found it, so a deletion is never sent for a server whose state
-- the sweep didn't judge.
INSERT INTO dns_record_removals(workspace_id,server_id,name,zone,record_id,record_type,content,reason,hold_id)
SELECT s.workspace_id,@server_id,@name,@zone,@record_id,@record_type,@content,@reason,sqlc.narg(hold_id)::uuid
FROM (SELECT 1) one LEFT JOIN servers s ON s.id = @server_id
WHERE (s.id IS NOT NULL) = @known::boolean
ON CONFLICT(zone,record_id) DO UPDATE SET content=EXCLUDED.content,reason=EXCLUDED.reason,requested_at=EXCLUDED.requested_at,hold_id=EXCLUDED.hold_id
WHERE dns_record_removals.confirmed_at IS NULL;

-- name: LockDNSZoneSweep :exec
-- Serializes the zone sweep's decisions about holds, and the operator's confirmation, per zone.
SELECT pg_advisory_xact_lock(hashtextextended('dns_zone_sweep:' || @zone::text,32));

-- name: LapseDNSWithdrawalHolds :exec
-- unscoped: installation-wide. A confirmation is good for a day; what it named and still stands
-- is judged afresh after that.
UPDATE dns_withdrawal_holds SET closed_at=clock_timestamp(),close_reason='lapsed'
WHERE zone = @zone AND closed_at IS NULL AND confirmed_at<clock_timestamp()-interval '24 hours';

-- name: CloseWithdrawnDNSWithdrawalHolds :exec
-- unscoped: installation-wide. A confirmed hold is done once every record it holds has been
-- removed from the provider's zone, by the sweep or by a publication's own withdrawal.
UPDATE dns_withdrawal_holds h SET closed_at=clock_timestamp(),close_reason='withdrawn'
WHERE h.zone = @zone AND h.closed_at IS NULL AND h.confirmed_at IS NOT NULL
    AND NOT EXISTS(SELECT 1 FROM dns_withdrawal_hold_records hr WHERE hr.hold_id=h.id
        AND NOT EXISTS(SELECT 1 FROM dns_record_removals r WHERE r.zone=h.zone AND r.record_id=hr.record_id AND r.confirmed_at IS NOT NULL));

-- name: AwaitingDNSWithdrawalHold :one
-- unscoped: installation-wide. At most one awaits confirmation.
SELECT id FROM dns_withdrawal_holds WHERE zone = @zone AND closed_at IS NULL AND confirmed_at IS NULL;

-- name: ConfirmedDNSWithdrawalHoldRecords :many
-- unscoped: installation-wide. The records the open confirmed holds hold, and which holds them.
SELECT r.record_id,r.hold_id FROM dns_withdrawal_hold_records r JOIN dns_withdrawal_holds h ON h.id=r.hold_id
WHERE h.zone = @zone AND h.closed_at IS NULL AND h.confirmed_at IS NOT NULL;

-- name: DNSWithdrawalHoldRecordIDs :many
-- unscoped: installation-wide.
SELECT record_id FROM dns_withdrawal_hold_records WHERE hold_id = @hold_id;

-- name: OpenDNSWithdrawalHold :one
-- unscoped: installation-wide.
INSERT INTO dns_withdrawal_holds(zone,records,marked) VALUES(@zone,@records,@marked) RETURNING id;

-- name: InsertDNSWithdrawalHoldRecords :copyfrom
-- unscoped: installation-wide.
INSERT INTO dns_withdrawal_hold_records(hold_id,record_id,server_id,workspace_id,name,record_type,content)
VALUES(@hold_id,@record_id,@server_id,@workspace_id,@name,@record_type,@content);

-- name: SeeDNSWithdrawalHold :exec
-- unscoped: installation-wide.
UPDATE dns_withdrawal_holds SET seen_at=clock_timestamp(),marked = @marked WHERE id = @id;

-- name: CloseDNSWithdrawalHold :exec
-- unscoped: installation-wide.
UPDATE dns_withdrawal_holds SET closed_at=clock_timestamp(),close_reason = @close_reason::text WHERE id = @id AND closed_at IS NULL;

-- name: ConfirmDNSWithdrawalHold :execrows
-- unscoped: the installation operator's command. Confirms exactly the hold the operator saw: it
-- must still await confirmation and hold as many records as they counted.
UPDATE dns_withdrawal_holds SET confirmed_at=clock_timestamp(),confirmed_by = @confirmed_by::text,confirmation_note = @note::text
WHERE id = @id AND closed_at IS NULL AND confirmed_at IS NULL AND records = @records;

-- name: ListDNSWithdrawalHolds :many
-- unscoped: the installation operator's command.
SELECT id,zone,records,marked,opened_at,seen_at,confirmed_at,confirmed_by,confirmation_note FROM dns_withdrawal_holds
WHERE closed_at IS NULL ORDER BY opened_at,id;

-- name: DNSWithdrawalHoldWorkspaces :many
-- unscoped: the installation operator's command. NULL is servers this database doesn't know.
SELECT workspace_id,count(*)::bigint AS records FROM dns_withdrawal_hold_records WHERE hold_id = @hold_id
GROUP BY workspace_id ORDER BY count(*) DESC,workspace_id NULLS LAST;

-- name: DNSWithdrawalHoldSample :many
-- unscoped: the installation operator's command.
SELECT record_id,server_id,workspace_id,name,record_type,content FROM dns_withdrawal_hold_records
WHERE hold_id = @hold_id ORDER BY name,record_type,record_id LIMIT @max;

-- name: WorkspaceHasHeldDNSRecords :one
-- Whether records of the workspace's servers await the operator's confirmation.
SELECT EXISTS(SELECT 1 FROM dns_withdrawal_hold_records r JOIN dns_withdrawal_holds h ON h.id=r.hold_id
    WHERE r.workspace_id = @workspace_id AND h.closed_at IS NULL AND h.confirmed_at IS NULL)::boolean AS held;

-- name: DNSWithdrawalHoldZone :one
-- unscoped: the installation operator's command.
SELECT zone FROM dns_withdrawal_holds WHERE id = @id;
