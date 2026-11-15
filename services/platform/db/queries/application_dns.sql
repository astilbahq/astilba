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
ON CONFLICT(zone,record_id) DO UPDATE SET content=EXCLUDED.content,reason=EXCLUDED.reason,requested_at=EXCLUDED.requested_at
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
-- A late name not yet recorded as a publication counts too.
SELECT ((SELECT count(*) FROM dns_publications WHERE NOT @automatic::boolean OR zone NOT IN ('',@zone::text))
    +(SELECT count(*) FROM dns_late_publications WHERE NOT @automatic::boolean OR zone NOT IN ('',@zone::text)))::bigint AS count;

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
SELECT d.workspace_id,d.base,d.ipv4,d.ipv6,i.id AS installation_id FROM server_domains d JOIN servers s ON s.id=d.server_id CROSS JOIN installation i
WHERE d.server_id = @server_id AND d.generation = @generation AND d.approved_controller = @controller AND s.controller_generation = @controller
    AND NOT s.revoked AND NOT s.awaiting_transfer AND d.base = @base;

-- name: ObserveServerDomain :exec
-- unscoped: a DNS job names the server. Ignored once the profile has moved on.
UPDATE server_domains SET state = @state,note = @note,observed_at=clock_timestamp(),records = @records
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
