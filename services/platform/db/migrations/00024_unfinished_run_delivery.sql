-- +goose Up
-- Earlier controllers delivered cancelled and expired pipelines without recording
-- it, so an unfinished run's false value does not prove no agent holds its command.
UPDATE pipeline_runs r SET delivery_started = NULL
FROM execution_operations o
WHERE o.workspace_id = r.workspace_id AND o.id = r.id AND r.delivery_started = false
 AND o.state IN ('dispatched', 'running', 'needs_attention');

-- +goose Down
