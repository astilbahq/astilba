-- +goose Up
-- Every name where this installation may have created provider records, written
-- before the provider is called. A row outlives its server and its domain profile,
-- and is removed only once the provider shows none of the installation's records
-- left at that name. Server IDs are never reused, because server rows are never deleted.
CREATE TABLE dns_publications (
 server_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 base text NOT NULL,
 -- '' marks a row recorded before zones were: it is treated as the configured zone.
 zone text NOT NULL DEFAULT '',
 -- Set by revocation. It stays set if the server is renewed or transferred, until the
 -- records are gone or an owner approves addresses for the server again.
 revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 checked_at timestamptz,
 PRIMARY KEY(server_id,base,zone)
);
CREATE INDEX dns_publications_sweep ON dns_publications(checked_at NULLS FIRST,server_id);
-- Any profile automation has touched may have records. A manual one never has.
INSERT INTO dns_publications(server_id,workspace_id,base,revoked)
SELECT d.server_id,d.workspace_id,d.base,s.revoked FROM server_domains d JOIN servers s ON s.id=d.server_id
WHERE d.state<>'manual' OR d.records<>'{}';
-- The audit of every provider record Astilba deleted. A row is written before the
-- deletion is sent, so a deletion whose response was lost is still recorded, and is
-- confirmed once the provider shows the record gone.
CREATE TABLE dns_record_removals (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 server_id uuid NOT NULL,
 name text NOT NULL,
 zone text NOT NULL,
 record_id text NOT NULL,
 record_type text NOT NULL CHECK(record_type IN ('A','AAAA')),
 content text NOT NULL,
 reason text NOT NULL CHECK(reason IN ('revoked','management_inactive','base_changed','address_removed')),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 confirmed_at timestamptz,
 UNIQUE(zone,record_id)
);
CREATE INDEX dns_record_removals_server ON dns_record_removals(server_id,name,zone) WHERE confirmed_at IS NULL;
-- +goose Down
DROP TABLE dns_record_removals;
DROP TABLE dns_publications;
