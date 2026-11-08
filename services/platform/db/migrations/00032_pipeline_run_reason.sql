-- +goose Up
-- Why the controller started a run, when that isn't a push or a person: shown with the run's
-- note whatever its runner reports.
ALTER TABLE pipeline_runs ADD COLUMN reason text NOT NULL DEFAULT '' CHECK(length(reason)<=500);

-- +goose Down
ALTER TABLE pipeline_runs DROP COLUMN reason;
