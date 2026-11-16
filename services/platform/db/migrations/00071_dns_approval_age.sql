-- +goose Up
-- astilba: not additive: it widens dns_record_removals' reason check, which the additive check can't verify.
-- A management restored from an older backup holds approvals the owner may have replaced since.
-- When the provider's zone shows a record of ours changed after the approval, the approval ends
-- rather than publishing its older addresses again. See docs/hosting.md#records-the-database-has-lost.

-- When an owner approved the addresses, by the database's clock. NULL for an approval given before
-- this was recorded, which counts as older than any record.
ALTER TABLE server_domains ADD COLUMN approved_at timestamptz;
-- The zone the records were observed in, which names their IDs.
ALTER TABLE server_domains ADD COLUMN observed_zone text NOT NULL DEFAULT '';

-- A record the zone holds at an approved name, changed after the approval and to something else,
-- is withdrawn by the zone sweep with this reason.
ALTER TABLE dns_record_removals DROP CONSTRAINT dns_record_removals_reason_check;
ALTER TABLE dns_record_removals ADD CONSTRAINT dns_record_removals_reason_check CHECK(reason IN ('revoked','management_inactive','base_changed','address_removed','unbacked','approval_outdated'));

-- +goose Down
DELETE FROM dns_record_removals WHERE reason = 'approval_outdated';
ALTER TABLE dns_record_removals DROP CONSTRAINT dns_record_removals_reason_check;
ALTER TABLE dns_record_removals ADD CONSTRAINT dns_record_removals_reason_check CHECK(reason IN ('revoked','management_inactive','base_changed','address_removed','unbacked'));
ALTER TABLE server_domains DROP COLUMN observed_zone;
ALTER TABLE server_domains DROP COLUMN approved_at;
