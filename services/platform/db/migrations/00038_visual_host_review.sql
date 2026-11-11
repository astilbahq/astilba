-- +goose Up
-- Whether applying a visual plan needs an owner to approve host changes it makes beyond what
-- the environment's applied deployment already has. Management keeps it beside the plan, not in
-- it, so the plan an agent verifies is unchanged. A request records what the planner found; its
-- plan adds what the agent reported when it resolved it. Both are written once, like the rows
-- they describe. Earlier requests and plans need review.
ALTER TABLE visual_requests ADD COLUMN host_review boolean NOT NULL DEFAULT true;
ALTER TABLE visual_plans ADD COLUMN host_review boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE visual_plans DROP COLUMN host_review;
ALTER TABLE visual_requests DROP COLUMN host_review;
