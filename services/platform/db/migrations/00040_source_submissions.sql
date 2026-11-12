-- +goose Up
-- Management submits a saved Git draft itself, through the provider's API, so a runner never
-- holds a credential that can write to the repository. Each row is one attempt; configuration is
-- the frozen draft, with no credential. Runner submissions before this stay in git_submissions.
CREATE TABLE source_submissions (
 id uuid PRIMARY KEY, workspace_id uuid NOT NULL, environment_id uuid NOT NULL, revision_id uuid NOT NULL,
 request_key uuid NOT NULL, actor_id uuid NOT NULL REFERENCES accounts(id),
 configuration jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','succeeded','failed')),
 note text NOT NULL DEFAULT '' CHECK(length(note)<=1024),
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz,
 CHECK((state='queued')=(finished_at IS NULL)),
 UNIQUE(workspace_id,id), UNIQUE(workspace_id,request_key),
 FOREIGN KEY(workspace_id,environment_id) REFERENCES environments(workspace_id,id),
 FOREIGN KEY(workspace_id,environment_id,revision_id) REFERENCES specification_revisions(workspace_id,environment_id,id)
);
CREATE UNIQUE INDEX source_submission_queued ON source_submissions(workspace_id,environment_id,revision_id) WHERE state='queued';
CREATE INDEX source_submission_history ON source_submissions(workspace_id,environment_id,created_at DESC,id DESC);

-- +goose Down
DROP TABLE source_submissions;
