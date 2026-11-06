-- +goose Up
-- The controller generation in which this member last claimed a network generation further
-- ahead than its in-flight work explains. Not exported: a restored or imported workspace has
-- a later controller generation, which is what entitles a host to make that claim again.
ALTER TABLE network_members ADD COLUMN history_claim_controller bigint NOT NULL DEFAULT 0 CHECK(history_claim_controller>=0);

-- +goose Down
ALTER TABLE network_members DROP COLUMN history_claim_controller;
