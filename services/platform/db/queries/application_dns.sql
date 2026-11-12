-- Application DNS publications: where this installation may have provider records,
-- and the audit of every record it deleted. See docs/hosting.md#generated-application-names.

-- name: DNSInstallation :one
SELECT id FROM dns_installation;

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
-- Written before the deletion is sent. A retry of the same deletion keeps the first row.
INSERT INTO dns_record_removals(workspace_id,server_id,name,zone,record_id,record_type,content,reason)
VALUES(@workspace_id,@server_id,@name,@zone,@record_id,@record_type,@content,@reason)
ON CONFLICT(zone,record_id) DO NOTHING;

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

-- name: StaleDNSPublicationServers :many
-- unscoped: the sweep serves every workspace. Rows in another zone are stranded and left out.
SELECT p.server_id,COALESCE(max(d.generation),0)::bigint AS generation,COALESCE(max(d.approved_controller),0)::bigint AS controller
FROM dns_publications p
LEFT JOIN server_domains d ON d.server_id=p.server_id
WHERE p.zone IN ('',@zone::text) AND (p.checked_at IS NULL OR p.checked_at<now()-interval '1 minute')
    AND (p.revoked OR p.base <> @base::text
        OR (@hosted::boolean AND NOT EXISTS (SELECT 1 FROM hosted_subscriptions h WHERE h.workspace_id=p.workspace_id AND h.management_until>now())))
GROUP BY p.server_id
ORDER BY min(COALESCE(p.checked_at,'-infinity'::timestamptz)),p.server_id
LIMIT 100;

-- name: CountStrandedDNSPublications :one
-- unscoped: an installation-wide startup check.
SELECT count(*) FROM dns_publications WHERE NOT @automatic::boolean OR zone NOT IN ('',@zone::text);
