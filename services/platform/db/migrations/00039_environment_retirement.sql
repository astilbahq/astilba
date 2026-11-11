-- +goose Up
-- Environments are never deleted, because revisions, plans and history refer to them.
-- Retirement is a lifecycle state instead; docs/environment-retirement.md describes it.
-- environment_state is named so that later states can be added to it.
-- retired_at and retired_by record the latest retirement request. retired_without_host
-- records that a revoked server could not be asked to remove anything.
ALTER TABLE environments
 ADD COLUMN state text NOT NULL DEFAULT 'active',
 ADD COLUMN retired_at timestamptz,
 ADD COLUMN retired_by uuid REFERENCES accounts(id),
 ADD COLUMN retirement_operation_id uuid,
 ADD COLUMN retired_without_host boolean NOT NULL DEFAULT false;
ALTER TABLE environments ADD CONSTRAINT environment_state CHECK(state IN ('active','retiring','retired'));
ALTER TABLE environments ADD CONSTRAINT environment_retirement_record CHECK(
 (state = 'active') = (retired_at IS NULL)
 AND (retired_at IS NULL) = (retired_by IS NULL)
 AND (state <> 'active' OR (retirement_operation_id IS NULL AND NOT retired_without_host))
 AND (state <> 'retiring' OR retirement_operation_id IS NOT NULL)
 AND (purpose <> 'preview' OR state = 'active'));
ALTER TABLE environments ADD CONSTRAINT retirement_operation_owner FOREIGN KEY(workspace_id,id,retirement_operation_id) REFERENCES execution_operations(workspace_id,environment_id,id) DEFERRABLE INITIALLY DEFERRED;

-- +goose Down
ALTER TABLE environments DROP CONSTRAINT retirement_operation_owner;
ALTER TABLE environments DROP CONSTRAINT environment_retirement_record;
ALTER TABLE environments DROP CONSTRAINT environment_state;
ALTER TABLE environments DROP COLUMN retired_without_host, DROP COLUMN retirement_operation_id, DROP COLUMN retired_by, DROP COLUMN retired_at, DROP COLUMN state;
