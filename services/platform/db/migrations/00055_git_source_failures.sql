-- +goose Up
-- astilba: not additive: it widens build_target_rejection with two reasons the previous release cannot show.
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
-- A build target whose runner must finish enrolling again or be updated before it can build waits
-- for it, for a day at most, and shows which runner it waits for and since when.
ALTER TABLE build_targets
    ADD COLUMN waiting_commit text,
    ADD COLUMN waiting_reason text,
    ADD COLUMN waiting_runner_id uuid,
    ADD COLUMN waiting_since timestamptz,
    ADD CONSTRAINT build_target_wait CHECK (
        (waiting_commit IS NULL AND waiting_reason IS NULL AND waiting_runner_id IS NULL AND waiting_since IS NULL) OR
        (length(waiting_commit) BETWEEN 1 AND 64 AND waiting_reason IN ('runner_update','runner_enrollment') AND waiting_runner_id IS NOT NULL AND waiting_since IS NOT NULL)
    );

-- +goose Down
ALTER TABLE build_targets DROP CONSTRAINT build_target_wait, DROP COLUMN waiting_since, DROP COLUMN waiting_runner_id, DROP COLUMN waiting_reason, DROP COLUMN waiting_commit;
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
