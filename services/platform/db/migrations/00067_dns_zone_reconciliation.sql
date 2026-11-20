-- +goose Up
-- astilba: not additive: an older release can't read the dns_withdrawal_held alert kind.
-- The zone sweep compares the provider's zone with the database and withdraws this installation's
-- records that nothing backs, such as those a management restored from an older backup never
-- knew it made. See docs/hosting.md#records-the-database-has-lost.

-- A record of a server this database has never heard of has no workspace. Its removal is audited
-- all the same, as unbacked.
ALTER TABLE dns_record_removals ALTER COLUMN workspace_id DROP NOT NULL;
ALTER TABLE dns_record_removals DROP CONSTRAINT dns_record_removals_reason_check;
ALTER TABLE dns_record_removals ADD CONSTRAINT dns_record_removals_reason_check CHECK(reason IN ('revoked','management_inactive','base_changed','address_removed','unbacked'));
-- The hold whose confirmation let the sweep remove the record, when it took one.
ALTER TABLE dns_record_removals ADD COLUMN hold_id uuid;

-- A sweep that would withdraw too many records at once withdraws none of them and holds them here,
-- until the installation operator confirms the hold. A hold's records never change: a sweep that
-- finds a record the hold doesn't hold closes it as superseded and opens a new one, so a
-- confirmation applies only to records the operator saw. At most one hold awaits confirmation per
-- zone; a confirmed hold stays open until its records are removed, or for a day.
CREATE TABLE dns_withdrawal_holds (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 zone text NOT NULL,
 records integer NOT NULL,
 -- The installation's marked address records the sweep listed, which the threshold compares with.
 marked integer NOT NULL,
 opened_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 confirmed_at timestamptz,
 confirmed_by text,
 confirmation_note text,
 closed_at timestamptz,
 close_reason text CHECK(close_reason IN ('superseded','cleared','withdrawn','lapsed')),
 CHECK((confirmed_at IS NULL) = (confirmed_by IS NULL)),
 CHECK((closed_at IS NULL) = (close_reason IS NULL))
);
CREATE UNIQUE INDEX dns_withdrawal_holds_awaiting ON dns_withdrawal_holds(zone) WHERE closed_at IS NULL AND confirmed_at IS NULL;
CREATE TABLE dns_withdrawal_hold_records (
 hold_id uuid NOT NULL REFERENCES dns_withdrawal_holds(id) ON DELETE CASCADE,
 record_id text NOT NULL,
 server_id uuid NOT NULL,
 -- NULL for a server this database has never heard of.
 workspace_id uuid,
 name text NOT NULL,
 record_type text NOT NULL CHECK(record_type IN ('A','AAAA')),
 content text NOT NULL,
 PRIMARY KEY(hold_id,record_id)
);
CREATE INDEX dns_withdrawal_hold_records_workspace ON dns_withdrawal_hold_records(workspace_id,hold_id) WHERE workspace_id IS NOT NULL;

-- Raised, on self-hosted installations, in each workspace with records awaiting the operator.
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window','dns_withdrawal_held'));

-- +goose Down
DELETE FROM operational_alerts WHERE kind = 'dns_withdrawal_held';
ALTER TABLE operational_alerts DROP CONSTRAINT operational_alerts_kind_check;
ALTER TABLE operational_alerts ADD CONSTRAINT operational_alerts_kind_check CHECK(kind IN ('server_offline','disk_docker','disk_agent','service_runtime','service_health','backup_schedule','deployment_problem','build_problem','backup_problem','network_problem','identity_conflict','recovery_window'));
DROP TABLE dns_withdrawal_hold_records;
DROP TABLE dns_withdrawal_holds;
ALTER TABLE dns_record_removals DROP COLUMN hold_id;
DELETE FROM dns_record_removals WHERE reason = 'unbacked' OR workspace_id IS NULL;
ALTER TABLE dns_record_removals DROP CONSTRAINT dns_record_removals_reason_check;
ALTER TABLE dns_record_removals ADD CONSTRAINT dns_record_removals_reason_check CHECK(reason IN ('revoked','management_inactive','base_changed','address_removed'));
ALTER TABLE dns_record_removals ALTER COLUMN workspace_id SET NOT NULL;
