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
 created_at timestamptz NOT NULL DEFAULT now(),
 checked_at timestamptz,
 PRIMARY KEY(server_id,base,zone)
);
CREATE INDEX dns_publications_sweep ON dns_publications(checked_at NULLS FIRST,server_id);
-- Any profile may have been reconciled before this table existed.
INSERT INTO dns_publications(server_id,workspace_id,base) SELECT server_id,workspace_id,base FROM server_domains;
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
 reason text NOT NULL CHECK(reason IN ('revoked','management_inactive','base_changed','server_missing','address_removed')),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 confirmed_at timestamptz,
 UNIQUE(zone,record_id)
);
CREATE INDEX dns_record_removals_server ON dns_record_removals(server_id,name,zone) WHERE confirmed_at IS NULL;
-- +goose Down
DROP TABLE dns_record_removals;
DROP TABLE dns_publications;
