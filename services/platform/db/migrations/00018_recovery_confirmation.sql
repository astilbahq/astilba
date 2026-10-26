-- +goose Up
ALTER TABLE accounts
 ADD COLUMN recovery_codes_saved boolean NOT NULL DEFAULT true,
 ADD COLUMN recovery_codes_generation uuid;

-- +goose Down
ALTER TABLE accounts
 DROP COLUMN recovery_codes_generation,
 DROP COLUMN recovery_codes_saved;
