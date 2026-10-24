-- +goose Up
ALTER TABLE servers ADD COLUMN controller_generation bigint NOT NULL DEFAULT 0 CHECK(controller_generation>=0);
ALTER TABLE servers ADD COLUMN awaiting_transfer boolean NOT NULL DEFAULT false;
-- +goose Down
ALTER TABLE servers DROP COLUMN awaiting_transfer, DROP COLUMN controller_generation;
