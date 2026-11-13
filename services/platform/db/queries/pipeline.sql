-- name: AllGitAccess :many
SELECT workspace_id,id,encrypted_access FROM git_connections FOR UPDATE;
-- name: RewrapGitAccess :exec
UPDATE git_connections SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllRegistryAccess :many
SELECT workspace_id,id,encrypted_access FROM registries FOR UPDATE;
-- name: RewrapRegistryAccess :exec
UPDATE registries SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllBuildSecrets :many
SELECT workspace_id,id,encrypted_value FROM build_secret_versions FOR UPDATE;
-- name: RewrapBuildSecret :exec
UPDATE build_secret_versions SET encrypted_value=$3 WHERE workspace_id=$1 AND id=$2;
-- name: AllGitHubAuthorizations :many
SELECT workspace_id,id,encrypted_access FROM github_app_authorizations FOR UPDATE;
-- name: RewrapGitHubAuthorization :exec
UPDATE github_app_authorizations SET encrypted_access=$3 WHERE workspace_id=$1 AND id=$2;

-- name: MarkPipelineDeliveryStarted :exec
UPDATE pipeline_runs SET delivery_started=true WHERE workspace_id = @workspace_id AND id = @id;
-- name: FinishUndeliveredPipeline :exec
UPDATE execution_operations o SET state = @state, note = COALESCE((SELECT r.reason || ' ' FROM pipeline_runs r WHERE r.workspace_id = o.workspace_id AND r.id = o.id AND r.reason <> ''), '') || @note::text
WHERE o.workspace_id = @workspace_id AND o.id = @id;
-- name: StopPossiblyDeliveredPipeline :exec
UPDATE execution_operations SET cancel_requested = true, note = @note::text || CASE WHEN state = 'needs_attention' AND note <> '' THEN ' ' || note ELSE '' END WHERE workspace_id = @workspace_id AND id = @id;

-- name: AutomaticBuildArtifact :one
SELECT identity FROM build_artifacts WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND id = @id;

-- name: EnvironmentHoldsAutomaticWork :one
-- An automatic deployment has no active operation between its passes, and a build admitted in that gap
-- would stack its image on a draft still waiting to deploy. The age limit keeps a job stuck in retries
-- from stopping automatic work for the environment indefinitely; a run admitted after it blocks for review.
SELECT (EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id = @workspace_id AND o.environment_id = @environment_id::uuid AND o.state NOT IN ('succeeded','failed','cancelled'))
    OR EXISTS(SELECT 1 FROM pipeline_auto_deployments d JOIN pipeline_runs r ON r.workspace_id = d.workspace_id AND r.id = d.id
              WHERE d.workspace_id = @workspace_id AND d.environment_id = @environment_id::uuid AND d.state = 'pending' AND r.created_at > now() - interval '1 hour'))::boolean AS busy;

-- name: AutomaticDeploymentWorkspace :one
-- unscoped: queued jobs carry only the deployment, as the other job lookups by ID do; the row
-- establishes the workspace. Remove this when jobs carry the workspace in their arguments.
SELECT workspace_id FROM pipeline_auto_deployments WHERE id = @id;

-- name: SetGitSourcePendingCommit :exec
UPDATE git_sources SET pending_commit = @commit
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: ClearGitSourcePendingCommit :exec
-- A conflict keeps its commit until an owner's refresh settles the draft.
UPDATE git_sources SET pending_commit = ''
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND status <> 'conflict' AND pending_commit <> '';

-- name: LatestImportIsAtCommit :one
SELECT COALESCE((
    SELECT configuration->>'commit' FROM pipeline_runs
    WHERE workspace_id = @workspace_id AND environment_id = @environment_id
        AND configuration->>'action' = 'import' AND created_at >= @since::timestamptz
    ORDER BY created_at DESC LIMIT 1
) = @commit::text, false)::boolean AS started;

-- name: SetPipelineRunReason :exec
-- The operation shows the reason until its runner first reports, which keeps it in front.
WITH run AS (UPDATE pipeline_runs r SET reason = @reason WHERE r.workspace_id = @workspace_id AND r.id = @id RETURNING r.id)
UPDATE execution_operations o SET note = @reason WHERE o.workspace_id = @workspace_id AND o.id IN (SELECT id FROM run);

-- name: PipelineRunReason :one
SELECT COALESCE((SELECT reason FROM pipeline_runs WHERE workspace_id = @workspace_id AND id = @id), '')::text;

-- name: LatestImportedCommits :many
-- The commits of an environment's two latest saved imports, newest first.
SELECT (r.configuration->>'commit')::text FROM pipeline_runs r JOIN execution_operations o ON o.workspace_id = r.workspace_id AND o.id = r.id
WHERE r.workspace_id = @workspace_id AND r.environment_id = @environment_id AND r.configuration->>'action' = 'import' AND o.state = 'succeeded'
ORDER BY r.created_at DESC LIMIT 2;

-- name: InsertSourceSubmission :one
INSERT INTO source_submissions(id,workspace_id,environment_id,revision_id,request_key,actor_id,configuration)
VALUES(@id,@workspace_id,@environment_id,@revision_id,@request_key,@actor_id,@configuration) RETURNING created_at;

-- name: SourceSubmissionByRequestKey :one
SELECT id,environment_id,revision_id,state,note,result,created_at FROM source_submissions WHERE workspace_id = @workspace_id AND request_key = @request_key;

-- name: LatestSourceSubmission :one
-- The newest attempt for the environment, or for one revision of it when revision_id is set.
SELECT id,environment_id,revision_id,configuration,state,note,result,created_at FROM source_submissions
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND (sqlc.narg(revision_id)::uuid IS NULL OR revision_id = sqlc.narg(revision_id)::uuid)
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: LockSourceSubmission :one
SELECT environment_id,revision_id,configuration,state,created_at FROM source_submissions WHERE workspace_id = @workspace_id AND id = @id FOR UPDATE;

-- name: FinishSourceSubmission :exec
UPDATE source_submissions SET state = @state, note = @note, result = sqlc.narg(result), finished_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND state = 'queued';

-- name: NoteSourceSubmission :exec
UPDATE source_submissions SET note = @note WHERE workspace_id = @workspace_id AND id = @id AND state = 'queued';

-- name: AwaitSubmittedMerge :exec
-- A source that needs renewed approval keeps needing it: a submission doesn't grant it. A source bound
-- again since the submission was made no longer imports the commit it proposed edits to.
UPDATE git_sources g SET status = 'awaiting_merge' FROM source_submissions s
WHERE g.workspace_id = @workspace_id AND g.environment_id = @environment_id AND g.status <> 'approval_required'
    AND s.workspace_id = g.workspace_id AND s.id = @submission_id AND g.imported_commit = s.configuration->>'commit';

-- name: SubmittedReviews :many
-- The reviews that proposed exactly these files on top of commit from this connection's branch,
-- newest first: management's own submissions and, before them, runners'. A number a result can't
-- name as a review, such as one from an imported archive, is left out rather than failing the query.
SELECT review FROM (
    SELECT (CASE WHEN s.result->>'reviewNumber' ~ '^[1-9][0-9]{0,8}$' THEN (s.result->>'reviewNumber')::int END)::int AS review, s.created_at FROM source_submissions s
    WHERE s.workspace_id = @workspace_id AND s.environment_id = @environment_id AND s.state = 'succeeded'
      AND s.configuration->>'commit' = @commit::text AND s.configuration#>>'{connection,id}' = @connection_id::text AND s.configuration->>'branch' = @branch::text
      AND (SELECT jsonb_object_agg(f->>'path',f->>'content') FROM jsonb_array_elements(s.configuration#>'{bundle,files}') f) = @files::jsonb
    UNION ALL
    SELECT (CASE WHEN r.result->>'reviewNumber' ~ '^[1-9][0-9]{0,8}$' THEN (r.result->>'reviewNumber')::int END)::int, r.created_at FROM git_submissions g JOIN pipeline_runs r ON r.id = g.operation_id JOIN execution_operations o ON o.id = r.id
    WHERE g.workspace_id = @workspace_id AND g.environment_id = @environment_id AND o.state = 'succeeded'
      AND r.configuration->>'commit' = @commit::text AND r.configuration#>>'{connection,id}' = @connection_id::text AND r.configuration->>'branch' = @branch::text
      AND (SELECT jsonb_object_agg(f->>'path',f->>'content') FROM jsonb_array_elements(r.configuration#>'{bundle,files}') f) = @files::jsonb
) reviews WHERE review > 0 ORDER BY created_at DESC;

-- name: RecordGitSourceFailure :exec
-- The commit stops waiting to import: it failed. A conflict keeps its commit until an owner's refresh
-- settles the draft.
UPDATE git_sources SET failed_commit = @commit::text, failure_reason = @reason::text, failed_at = clock_timestamp(),
    pending_commit = CASE WHEN status = 'conflict' THEN pending_commit ELSE '' END
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: ClearGitSourceFailure :exec
-- An import admitted for a source ends its failure.
UPDATE git_sources SET failed_commit = '', failure_reason = '', failed_at = NULL
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND failed_commit <> '';

-- name: BindGitSource :exec
-- Binding a source again replaces all it was, as a first binding sets it: its approver, and the import
-- it compares drafts with, which the next import sets. An environment runs one operation at a time,
-- so no import for what the source was bound to before is still running.
INSERT INTO git_sources (workspace_id, environment_id, connection_id, configuration, policy_actor, policy_auth_version)
VALUES (@workspace_id, @environment_id, @connection_id, @configuration, @policy_actor, @policy_auth_version::bigint)
ON CONFLICT (workspace_id, environment_id) DO UPDATE SET connection_id = EXCLUDED.connection_id, configuration = EXCLUDED.configuration,
    policy_actor = EXCLUDED.policy_actor, policy_auth_version = EXCLUDED.policy_auth_version,
    imported_revision_id = NULL, imported_commit = '', pending_commit = '', status = 'pending',
    failed_commit = '', failure_reason = '', failed_at = NULL;

-- name: UnbindGitSource :execrows
DELETE FROM git_sources WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: MarkGitSourceConflict :exec
-- A conflict says why the commit waits, so an earlier failure no longer does.
UPDATE git_sources SET pending_commit = @commit::text, status = 'conflict', failed_commit = '', failure_reason = '', failed_at = NULL
WHERE workspace_id = @workspace_id AND environment_id = @environment_id;

-- name: RejectBuildTarget :exec
UPDATE build_targets SET rejected_commit = @commit::text, rejection_reason = @reason::text, rejected_at = clock_timestamp()
WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND application_id = @application_id;

-- name: RunnerReenrolling :one
-- A runner that is enrolling again, or moving to another management, has no credential until it
-- finishes, but it is still the workspace's: work for it waits rather than fails.
SELECT EXISTS(SELECT 1 FROM servers WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]) AND NOT revoked AND credential_hash IS NULL)::boolean AS reenrolling;

-- name: AutomaticBuildsBehind :one
-- Whether an environment builds this branch automatically and has not yet been asked to build commit.
SELECT EXISTS(SELECT 1 FROM build_targets WHERE workspace_id = @workspace_id AND environment_id = @environment_id AND connection_id = @connection_id
    AND configuration->>'branch' = @branch::text AND configuration->>'autoBuild' = 'true' AND automatic_requested_commit <> @commit::text)::boolean AS behind;
