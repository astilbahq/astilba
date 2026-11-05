-- +goose Up
-- Records the latest push delivery per branch, so that a head a provider gave before a delivery
-- is not used after it. Values never repeat, so removing an old row can only cause a question to
-- be asked again. Rows are local to this controller, so archives leave them out.
CREATE SEQUENCE git_branch_push_sequence;
CREATE TABLE git_branch_pushes (
 workspace_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 branch text NOT NULL,
 sequence bigint NOT NULL DEFAULT nextval('git_branch_push_sequence'),
 pushed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,connection_id,branch),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES git_connections(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX git_branch_pushes_retention ON git_branch_pushes(pushed_at);

-- +goose Down
DROP TABLE git_branch_pushes;
DROP SEQUENCE git_branch_push_sequence;
