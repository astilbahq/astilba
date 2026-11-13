-- +goose Up
-- astilba: not additive: a release before it reads an erased secret version as ciphertext, which fails key rotation and deployment, and knows no purged state, so it would restore a purged environment.
-- Purging deletes a retired environment's retained data volumes and erases its secret values.
-- docs/environment-retirement.md describes it. Both purge states are one-way: a purged
-- environment is never restored, and a purge that did not finish is retried, not undone.
-- purged_at and purged_by record the purge request, purge_operation_id the command that removes
-- the volumes from the server, and purged_without_host that a revoked server could not be asked.
ALTER TABLE environments
 ADD COLUMN purged_at timestamptz,
 ADD COLUMN purged_by uuid REFERENCES accounts(id),
 ADD COLUMN purge_operation_id uuid,
 ADD COLUMN purged_without_host boolean NOT NULL DEFAULT false;
ALTER TABLE environments DROP CONSTRAINT environment_state;
ALTER TABLE environments ADD CONSTRAINT environment_state CHECK(state IN ('active','retiring','retired','purging','purged'));
ALTER TABLE environments ADD CONSTRAINT environment_purge_record CHECK(
 (state IN ('purging','purged')) = (purged_at IS NOT NULL)
 AND (purged_at IS NULL) = (purged_by IS NULL)
 AND (state IN ('purging','purged') OR (purge_operation_id IS NULL AND NOT purged_without_host))
 AND (state <> 'purging' OR purge_operation_id IS NOT NULL));
ALTER TABLE environments ADD CONSTRAINT purge_operation_owner FOREIGN KEY(workspace_id,id,purge_operation_id) REFERENCES execution_operations(workspace_id,environment_id,id) DEFERRABLE INITIALLY DEFERRED;

-- Specifications refer to secret versions forever, so retiring a secret or purging its
-- environment keeps each version's row and erases only its value. A retired secret's name can be
-- used again.
ALTER TABLE secret_versions ALTER COLUMN encrypted_value DROP NOT NULL;
ALTER TABLE secret_versions ADD COLUMN erased_at timestamptz;
ALTER TABLE secret_versions ADD CONSTRAINT secret_version_value CHECK((encrypted_value IS NULL) = (erased_at IS NOT NULL));
ALTER TABLE secrets ADD COLUMN retired_at timestamptz;
ALTER TABLE secrets DROP CONSTRAINT secrets_workspace_id_environment_id_name_key;
CREATE UNIQUE INDEX secrets_current_name ON secrets(workspace_id,environment_id,name) WHERE retired_at IS NULL;

-- +goose Down
DROP INDEX secrets_current_name;
ALTER TABLE secrets ADD CONSTRAINT secrets_workspace_id_environment_id_name_key UNIQUE(workspace_id,environment_id,name);
ALTER TABLE secrets DROP COLUMN retired_at;
ALTER TABLE secret_versions DROP CONSTRAINT secret_version_value;
ALTER TABLE secret_versions DROP COLUMN erased_at;
ALTER TABLE secret_versions ALTER COLUMN encrypted_value SET NOT NULL;
ALTER TABLE environments DROP CONSTRAINT purge_operation_owner;
ALTER TABLE environments DROP CONSTRAINT environment_purge_record;
ALTER TABLE environments DROP CONSTRAINT environment_state;
ALTER TABLE environments ADD CONSTRAINT environment_state CHECK(state IN ('active','retiring','retired'));
ALTER TABLE environments DROP COLUMN purged_without_host, DROP COLUMN purge_operation_id, DROP COLUMN purged_by, DROP COLUMN purged_at;
