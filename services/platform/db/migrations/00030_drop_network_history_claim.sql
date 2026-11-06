-- +goose Up
-- A later controller counts network generations for itself, so no host needs to claim history
-- that a restored backup has forgotten.
ALTER TABLE network_members DROP COLUMN history_claim_controller;

-- +goose Down
ALTER TABLE network_members ADD COLUMN history_claim_controller bigint NOT NULL DEFAULT 0 CHECK(history_claim_controller>=0);
