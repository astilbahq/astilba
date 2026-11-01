-- +goose Up
ALTER TABLE build_targets
 ADD COLUMN rejected_commit text,
 ADD COLUMN rejection_reason text,
 ADD COLUMN rejected_at timestamptz,
 ADD CONSTRAINT build_target_rejection CHECK (
  (rejected_commit IS NULL AND rejection_reason IS NULL AND rejected_at IS NULL) OR
  (rejected_commit IS NOT NULL AND rejection_reason IS NOT NULL AND length(rejected_commit) BETWEEN 1 AND 64 AND rejection_reason IN
   ('missing_build_secret','build_secret_budget','arguments_contain_secret','runtime_secret_budget') AND rejected_at IS NOT NULL)
 );

-- NULL preserves uncertainty for existing runs and imports. Only new runs are
-- explicitly known not to have reached an agent.
ALTER TABLE pipeline_runs ADD COLUMN delivery_started boolean;

-- +goose Down
ALTER TABLE pipeline_runs DROP COLUMN delivery_started;
ALTER TABLE build_targets DROP CONSTRAINT build_target_rejection,
 DROP COLUMN rejected_commit, DROP COLUMN rejection_reason, DROP COLUMN rejected_at;
