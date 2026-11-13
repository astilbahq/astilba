-- +goose Up
-- A Git source records why a refresh could not import a commit, as a build target records why
-- it rejected one: the commit, a bounded reason and when. A refresh does not try that source
-- again at that commit; a new push, a refresh started by hand or a rebind does.
ALTER TABLE git_sources
    ADD COLUMN failed_commit text NOT NULL DEFAULT '',
    ADD COLUMN failure_reason text NOT NULL DEFAULT '' CHECK (length(failure_reason) <= 1000),
    ADD COLUMN failed_at timestamptz;
-- A build target whose runner or registry is gone records that as it records refused inputs, so it
-- no longer stops the refresh of every other target and source on its branch.
ALTER TABLE build_targets DROP CONSTRAINT build_target_rejection, ADD CONSTRAINT build_target_rejection CHECK (
    (rejected_commit IS NULL AND rejection_reason IS NULL AND rejected_at IS NULL) OR
    (rejected_commit IS NOT NULL AND rejection_reason IS NOT NULL AND length(rejected_commit) BETWEEN 1 AND 64 AND rejection_reason IN
        ('missing_build_secret','build_secret_budget','arguments_contain_secret','runtime_secret_budget','runner_unavailable','registry_unavailable') AND rejected_at IS NOT NULL)
);
INSERT INTO schema_additive_migrations (version) VALUES (50);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 50;
-- The earlier release knows neither reason. Its next refresh of the target meets the same runner or
-- registry and stops its branch's refresh again, which is how it behaved.
UPDATE build_targets SET rejected_commit = NULL, rejection_reason = NULL, rejected_at = NULL
WHERE rejection_reason IN ('runner_unavailable','registry_unavailable');
ALTER TABLE build_targets DROP CONSTRAINT build_target_rejection, ADD CONSTRAINT build_target_rejection CHECK (
    (rejected_commit IS NULL AND rejection_reason IS NULL AND rejected_at IS NULL) OR
    (rejected_commit IS NOT NULL AND rejection_reason IS NOT NULL AND length(rejected_commit) BETWEEN 1 AND 64 AND rejection_reason IN
        ('missing_build_secret','build_secret_budget','arguments_contain_secret','runtime_secret_budget') AND rejected_at IS NOT NULL)
);
ALTER TABLE git_sources DROP COLUMN failed_at, DROP COLUMN failure_reason, DROP COLUMN failed_commit;
