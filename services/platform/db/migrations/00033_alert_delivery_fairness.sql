-- +goose Up
-- Delivery passes over a workspace's webhook while it holds a lease, so it looks up leases by
-- workspace and channel for each webhook delivery it considers.
CREATE INDEX alert_delivery_leases ON alert_deliveries(workspace_id,channel,next_attempt_at) WHERE state='leased';
-- Why the latest attempt failed, in words the controller chose, so an owner can see that a
-- receiver timed out or refused. It never holds the destination or what the receiver said.
ALTER TABLE alert_deliveries ADD COLUMN failure text NOT NULL DEFAULT '' CHECK(length(failure)<=200);

-- +goose Down
ALTER TABLE alert_deliveries DROP COLUMN failure;
DROP INDEX alert_delivery_leases;
