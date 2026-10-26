-- +goose Up
ALTER TABLE execution_operations DROP CONSTRAINT execution_operations_kind_check;
ALTER TABLE execution_operations ADD CONSTRAINT execution_operations_kind_check CHECK(kind IN ('test','update','visual','pipeline','backup','schedule','upgrade'));
