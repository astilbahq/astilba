-- +goose Up
ALTER TABLE execution_operations ADD CONSTRAINT execution_environment_identity UNIQUE(workspace_id,environment_id,id);
ALTER TABLE visual_plans ADD CONSTRAINT visual_plan_environment_identity UNIQUE(workspace_id,environment_id,id);
ALTER TABLE execution_operations ADD COLUMN runtime_outcome text;
ALTER TABLE execution_operations ADD COLUMN runtime_plan_id uuid;
ALTER TABLE execution_operations ADD CONSTRAINT execution_runtime_outcome CHECK (
 (runtime_outcome IS NULL AND runtime_plan_id IS NULL) OR
 (runtime_outcome IS NOT NULL AND ((runtime_outcome = 'applied' AND runtime_plan_id IS NOT NULL) OR
 (runtime_outcome = 'removed' AND runtime_plan_id IS NULL)))
);
ALTER TABLE execution_operations ADD CONSTRAINT execution_runtime_plan_owner FOREIGN KEY(workspace_id,environment_id,runtime_plan_id) REFERENCES visual_plans(workspace_id,environment_id,id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE environments ADD COLUMN runtime_operation_id uuid;
ALTER TABLE environments ADD CONSTRAINT runtime_operation_owner FOREIGN KEY(workspace_id,id,runtime_operation_id) REFERENCES execution_operations(workspace_id,environment_id,id) DEFERRABLE INITIALLY DEFERRED;

-- +goose Down
ALTER TABLE environments DROP CONSTRAINT runtime_operation_owner;
ALTER TABLE environments DROP COLUMN runtime_operation_id;
ALTER TABLE execution_operations DROP CONSTRAINT execution_runtime_plan_owner;
ALTER TABLE execution_operations DROP CONSTRAINT execution_runtime_outcome;
ALTER TABLE execution_operations DROP COLUMN runtime_plan_id;
ALTER TABLE execution_operations DROP COLUMN runtime_outcome;
ALTER TABLE visual_plans DROP CONSTRAINT visual_plan_environment_identity;
ALTER TABLE execution_operations DROP CONSTRAINT execution_environment_identity;
