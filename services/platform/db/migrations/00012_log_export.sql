-- +goose Up
CREATE INDEX pipeline_log_export ON pipeline_runs(workspace_id,environment_id,created_at DESC,id)
 WHERE result ? 'logs';
