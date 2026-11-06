-- +goose Up
-- Agent reports and network saves read workspace_networks and then write network_members.
-- Taking the locks in that order keeps this from deadlocking with them if it runs while
-- serving. A revocation at that moment can still deadlock, and either can then be retried.
ALTER TABLE workspace_networks ADD COLUMN reconcile_failing_since timestamptz;
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem'));
-- The coordinated scheduler now claims policies one at a time.
CREATE INDEX backup_policy_coordinated_scan ON backup_policies(coordinated_checked_at,id) WHERE configuration->>'coordinated'='true' AND configuration->>'enabled'='true';
-- Agents generate a fresh key for each membership, so a key held by two enabled members
-- can only come from a modified agent or archive, and no manifest can be built for that
-- network. A key stays with the one holder shown to be installed under it, by its own
-- manifest or a peer's: peers remove any member without a key, so clearing that holder
-- would cut its working links. No valid manifest could name a later holder, so that one
-- has no links to lose. It identifies again, and is refused with this note until it
-- reports a key of its own. Where no holder or several are shown installed, the evidence
-- cannot choose: all identify again and the first to report keeps the key. Generations
-- stay as they are because these networks have not converged since the duplicate arrived.
-- Any JSON shape is tolerated here: a stored document must not be able to stop a deploy.
WITH holders AS (
 SELECT m.network_id,m.server_id,m.public_key,EXISTS(SELECT 1 FROM network_members o WHERE o.network_id=m.network_id AND (
  (o.server_id=m.server_id AND o.installed_manifest->>'publicKey'=m.public_key)
  OR o.installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('membershipId',m.membership_id,'publicKey',m.public_key)))) AS installed
 FROM network_members m WHERE m.enabled AND m.public_key<>''
), shared AS (
 SELECT network_id,public_key,count(*) FILTER(WHERE installed) AS proven FROM holders GROUP BY network_id,public_key HAVING count(*)>1
)
UPDATE network_members m SET public_key='',note='This server reported the same private network key as another member. It must report its own key before private links can finish.'
FROM holders h JOIN shared s ON s.network_id=h.network_id AND s.public_key=h.public_key
WHERE m.network_id=h.network_id AND m.server_id=h.server_id AND NOT (h.installed AND s.proven=1);
CREATE UNIQUE INDEX network_member_active_key ON network_members(network_id,public_key) WHERE enabled AND public_key<>'';

-- +goose Down
DROP INDEX backup_policy_coordinated_scan;
DELETE FROM operational_alerts WHERE kind='network_problem';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem'));
ALTER TABLE workspace_networks DROP COLUMN reconcile_failing_since;
DROP INDEX network_member_active_key;
