-- +goose Up
-- A command's secret values are released to an agent instance once per dispatch, and again only
-- while that instance has not acknowledged holding them, or when it asks with a reason. One row
-- per (operation, dispatch epoch, agent instance) says where that stands. The digest names the
-- values' IDs and ciphertext, never their content. Each release is recorded in secret_access.
CREATE TABLE secret_releases (
    operation_id uuid NOT NULL REFERENCES execution_operations (id) ON DELETE CASCADE,
    dispatch_epoch bigint NOT NULL CHECK (dispatch_epoch >= 0),
    agent_instance text NOT NULL CHECK (length(agent_instance) BETWEEN 1 AND 200),
    workspace_id uuid NOT NULL,
    server_id uuid NOT NULL,
    digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    attempts integer NOT NULL CHECK (attempts >= 1),
    last_attempt_at timestamptz NOT NULL,
    acknowledged_at timestamptz,
    -- The instance said it lost what it acknowledged, so the next release is a re-delivery.
    redelivery boolean NOT NULL DEFAULT false,
    PRIMARY KEY (operation_id, dispatch_epoch, agent_instance)
);

-- A recovery that dispatches an operation again starts a new epoch, so the host that receives
-- it gets its values again.
ALTER TABLE execution_operations ADD COLUMN dispatch_epoch bigint NOT NULL DEFAULT 0;
INSERT INTO schema_additive_migrations (version) VALUES (45);

-- +goose Down
DELETE FROM schema_additive_migrations WHERE version = 45;
ALTER TABLE execution_operations DROP COLUMN dispatch_epoch;
DROP TABLE secret_releases;
