package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type refreshArgs struct {
	Workspace  uuid.UUID `json:"workspace" river:"unique"`
	Connection uuid.UUID `json:"connection" river:"unique"`
	Branch     string    `json:"branch" river:"unique"`
}

func (refreshArgs) Kind() string                 { return "platform_git_refresh_v1" }
func (refreshArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: gitQueue} }

// enqueueGitRefresh starts a refresh for a pushed branch that a source imports or a target builds
// automatically. The caller holds the connection's delivery lock. A workspace can sign pushes for
// any branch name, so one that nothing tracks starts nothing, and the jobs a workspace can have
// are one per branch its own configuration tracks.
func (s *Service) enqueueGitRefresh(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, p pipeline.Push) error {
	if err := s.Store.ManagementAdmission(ctx, tx, ws); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			return nil
		}
		return err
	}
	// An admission waiting on this branch's head from before the push must ask again, whether
	// or not it reaches the row this job looks for before the job runs.
	if err := db.New(tx).RecordGitBranchPush(ctx, db.RecordGitBranchPushParams{WorkspaceID: ws, ConnectionID: id, Branch: p.Branch}); err != nil {
		return err
	}
	tracked, err := db.New(tx).GitBranchTracked(ctx, db.GitBranchTrackedParams{WorkspaceID: ws, ConnectionID: id, Branch: p.Branch})
	if err != nil || !tracked {
		return err
	}
	return s.insertGitJob(ctx, tx, workspaceMetadata{Workspace: ws}, refreshArgs{ws, id, p.Branch}, refreshOpts, false)
}

// refreshOpts coalesces refreshes of a branch while one is waiting or running.
var refreshOpts = river.InsertOpts{MaxAttempts: 100, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}

type refreshWorker struct {
	river.WorkerDefaults[refreshArgs]
	service *Service
}

func (w *refreshWorker) Work(ctx context.Context, job *river.Job[refreshArgs]) error {
	if err := w.service.workspaceTurn(ctx, job.JobRow, job.Args.Workspace); err != nil {
		return err
	}
	return w.service.withGit(ctx, job.Args.Workspace, w.service.brokerKeys(secretbroker.System("git-refresh"), job.Args.Workspace), func(git *gitAnswers) error { return w.work(ctx, git, job) })
}

// A later push for the branch joins this job rather than starting another, so a source waiting
// to be read looks again often, and gives up in time for a new refresh to take over. Work waiting
// for its runner to finish enrolling or to be updated waits as long, then is refused, saying why.
// docs/git-builds.md states the limit.
const (
	sourceRetryDelay = time.Minute
	refreshWaitLimit = 24 * time.Hour
)

// work holds the workspace turn and the connection only between provider calls. The pass
// that admits work re-reads the sources, drafts, capacity and policy the head was fetched for.
func (w *refreshWorker) work(ctx context.Context, git *gitAnswers, job *river.Job[refreshArgs]) error {
	a := job.Args
	// Match interactive mutation's workspace lock before inspecting membership and drafts.
	tx, done, err := w.service.Store.BeginWorkspaceWrite(ctx, a.Workspace)
	if err != nil {
		return err
	}
	defer done()
	if err = w.service.Store.ManagementAdmission(ctx, tx, a.Workspace); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			// The job ends without looking, so the waits it recorded end with it.
			if err = db.New(tx).EndBranchBuildWaits(ctx, db.EndBranchBuildWaitsParams{WorkspaceID: a.Workspace, ConnectionID: a.Connection, Branch: a.Branch}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM git_connections WHERE workspace_id=$1 AND id=$2 FOR UPDATE", a.Workspace, a.Connection); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND state NOT IN ('succeeded','failed','cancelled')", a.Workspace).Scan(&active); err != nil {
		return err
	}
	capacity := 50 - active
	resolved, err := git.head(ctx, tx, a.Connection, a.Branch)
	if err != nil {
		return err
	}
	// A branch that is gone has nothing to import or build. The job asks once more, since a
	// provider can briefly miss a branch that a push has just created, and then ends. It ends in
	// this transaction, under the connection lock, so a push that recreates the branch while it
	// ends waits for it and then starts a refresh of its own.
	if errors.Is(resolved.Err, pipeline.ErrBranchNotFound) && job.Attempt > 1 {
		if err = db.New(tx).EndBranchBuildWaits(ctx, db.EndBranchBuildWaitsParams{WorkspaceID: a.Workspace, ConnectionID: a.Connection, Branch: a.Branch}); err != nil {
			return err
		}
		if _, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if resolved.Err != nil {
		return resolved.Err
	}
	connection, head := resolved.Connection, resolved.Value
	// One statement, so a source and the import that is saving it are read as of one moment.
	rows, err := tx.Query(ctx, `SELECT g.environment_id,g.configuration,g.imported_revision_id,g.imported_commit,e.project_id,e.draft_revision_id,g.policy_actor,g.pending_commit,g.failed_commit,
 EXISTS(SELECT 1 FROM pipeline_runs r JOIN execution_operations o ON o.workspace_id=r.workspace_id AND o.id=r.id WHERE r.workspace_id=g.workspace_id AND r.environment_id=g.environment_id
  AND r.configuration->>'action'='import' AND r.configuration->>'commit'<>$4 AND o.state NOT IN ('succeeded','failed','cancelled'))
 FROM git_sources g JOIN environments e ON e.workspace_id=g.workspace_id AND e.id=g.environment_id
 WHERE g.workspace_id=$1 AND g.connection_id=$2 AND g.status<>'approval_required' AND g.configuration->>'branch'=$3
  AND policy_approver_holds(g.workspace_id,g.policy_actor,g.policy_auth_version,true) AND e.state='active'
 ORDER BY g.pending_commit=$4,g.environment_id`, a.Workspace, a.Connection, a.Branch, head)
	if err != nil {
		return err
	}
	sources := []refreshedSource{}
	for rows.Next() {
		var v refreshedSource
		if err = rows.Scan(&v.env, &v.raw, &v.imported, &v.commit, &v.pid, &v.base, &v.actor, &v.pending, &v.failed, &v.importing); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	waiting := false
	// Sources share the operation's time in this order, so one that used it up last time goes
	// after those it held back. Sources also share the operation's pass limit however many there are, so each edited source's
	// question joins one call instead of taking a pass.
	var unanswered *providerCall
	// A source whose files cannot be read, or whose runner or workspace can't take an import yet,
	// waits for this job to look again, each minute for a day. Work elsewhere starts now, but its
	// environment's builds wait for it, because they build on the imported draft. A build target
	// whose runner is enrolling again, or must be updated before it can build, waits the same way
	// and says so.
	lookAgain := false
	waitingToImport := map[uuid.UUID]bool{}
	// Past its limit the job stops reading a source already shown as waiting, whatever else keeps
	// it alive, and shows any source it did not reach before it ends.
	deadline := job.CreatedAt.Add(refreshWaitLimit)
	expired := time.Now().After(deadline)
	for _, v := range sources {
		// The branch can move back to the source's commit while another commit imports, as
		// when a force-push removes it. That import would leave the source on a commit the
		// branch no longer holds, so the job waits for it and then imports the head.
		if v.commit == head && v.importing {
			waiting = true
			continue
		}
		// A failure at a commit the branch no longer holds no longer applies.
		if v.commit == head {
			if err = db.New(tx).ClearGitSourcePendingCommit(ctx, db.ClearGitSourcePendingCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env}); err != nil {
				return err
			}
			if err = db.New(tx).ClearGitSourceFailure(ctx, db.ClearGitSourceFailureParams{WorkspaceID: a.Workspace, EnvironmentID: v.env}); err != nil {
				return err
			}
			continue
		}
		// A source that failed at this commit would fail the same way again: a push, a refresh
		// started by hand or binding the source again tries again. Its environment's builds wait
		// for it, because they build on the imported draft.
		if v.failed == head {
			waitingToImport[v.env] = true
			continue
		}
		// A retry or wait must not repeat an import this job already started, whatever became of
		// it, and needs no more of the provider's time for this source. Only the job's latest
		// counts, so a branch moved back to an earlier commit imports that commit again.
		started, err := db.New(tx).LatestImportIsAtCommit(ctx, db.LatestImportIsAtCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env, Since: job.CreatedAt, Commit: head})
		if err != nil {
			return err
		}
		if started {
			continue
		}
		if capacity <= 0 {
			waiting = true
			continue
		}
		busy, err := db.New(tx).EnvironmentHoldsAutomaticWork(ctx, db.EnvironmentHoldsAutomaticWorkParams{WorkspaceID: a.Workspace, EnvironmentID: v.env})
		if err != nil {
			return err
		}
		if busy {
			waiting = true
			continue
		}
		// What fails for one source, such as its runner being revoked or a selected file being
		// renamed upstream, rolls back only that source's writes and is recorded on it, so the
		// branch's other sources and builds go on.
		savepoint, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		outcome, err := w.refreshSource(ctx, savepoint, git, job, connection, head, expired, v)
		if err != nil {
			if rollbackErr := savepoint.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			var call *providerCall
			if errors.As(err, &call) {
				unanswered = unanswered.and(call)
				continue
			}
			if errors.Is(err, errRefreshWaits) {
				if err = db.New(tx).SetGitSourcePendingCommit(ctx, db.SetGitSourcePendingCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env, Commit: head}); err != nil {
					return err
				}
				lookAgain = true
				waitingToImport[v.env] = true
				continue
			}
			var failed *sourceFailure
			if !errors.As(err, &failed) {
				return err
			}
			if err = db.New(tx).RecordGitSourceFailure(ctx, db.RecordGitSourceFailureParams{Commit: head, Reason: failed.reason, WorkspaceID: a.Workspace, EnvironmentID: v.env}); err != nil {
				return err
			}
			waitingToImport[v.env] = true
			continue
		}
		if err = savepoint.Commit(ctx); err != nil {
			return err
		}
		switch outcome {
		case sourceAdmitted:
			capacity--
		case sourceUnreadable:
			lookAgain = true
			waitingToImport[v.env] = true
		case sourceHeld:
			waitingToImport[v.env] = true
		}
	}
	if unanswered != nil {
		return unanswered
	}
	// Each target compares against current provider state, so delivery order cannot roll it backwards.
	rows, err = tx.Query(ctx, `SELECT b.environment_id,b.application_id,b.configuration,e.project_id,e.draft_revision_id,b.automatic_requested_commit
 FROM build_targets b JOIN environments e ON e.workspace_id=b.workspace_id AND e.id=b.environment_id
 WHERE b.workspace_id=$1 AND b.connection_id=$2 AND b.configuration->>'branch'=$3 AND b.configuration->>'autoBuild'='true'
  AND policy_approver_holds(b.workspace_id,b.policy_actor,b.policy_auth_version,COALESCE((b.configuration->>'autoDeploy')::boolean,false)) AND e.state='active'`, a.Workspace, a.Connection, a.Branch)
	if err != nil {
		return err
	}
	type target struct {
		env, app, pid, base uuid.UUID
		raw                 []byte
		commit              string
	}
	targets := []target{}
	for rows.Next() {
		var v target
		if err = rows.Scan(&v.env, &v.app, &v.raw, &v.pid, &v.base, &v.commit); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range targets {
		if v.commit == head || waitingToImport[v.env] {
			continue
		}
		if capacity <= 0 {
			waiting = true
			continue
		}
		busy, err := db.New(tx).EnvironmentHoldsAutomaticWork(ctx, db.EnvironmentHoldsAutomaticWorkParams{WorkspaceID: a.Workspace, EnvironmentID: v.env})
		if err != nil {
			return err
		}
		if busy {
			waiting = true
			continue
		}
		var cfg pipeline.BuildConfig
		if err = json.Unmarshal(v.raw, &cfg); err != nil {
			return err
		}
		env, err := loadVisualEnvironment(ctx, tx, a.Workspace, v.pid, v.env)
		if err != nil {
			return err
		}
		found := false
		for _, app := range env.Spec.Applications {
			found = found || app.ID == v.app
		}
		if !found {
			continue
		}
		// A target whose runner or registry is gone records why rather than stopping the branch.
		runner, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, cfg.Architecture, cfg.NeedsControls(), cfg.RailpackPins(), true)
		wait, waitErr := runnerWaits(ctx, tx, a.Workspace, cfg.RunnerID, err)
		if waitErr != nil {
			return waitErr
		}
		// Only the primary is refused here: a fallback that can't take the build is passed over.
		if wait || runnerUpgradeRequired(err) {
			waits, err := waitForRunner(ctx, tx, a.Workspace, v.env, v.app, head, cfg.RunnerID, runnerWait(wait), deadline)
			if err != nil {
				return err
			}
			lookAgain = lookAgain || waits
			continue
		}
		if runnerUnavailable(err) {
			if err = rejectTarget(ctx, tx, a.Workspace, v.env, v.app, head, "runner_unavailable"); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		var registry *pipeline.Registry
		if cfg.RegistryID != nil {
			r, _, err := loadRegistry(ctx, tx, a.Workspace, *cfg.RegistryID)
			if errors.Is(err, pgx.ErrNoRows) {
				if err = rejectTarget(ctx, tx, a.Workspace, v.env, v.app, head, "registry_unavailable"); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			registry = &r
		}
		compare, err := successfulBuildBaseline(ctx, tx, a.Workspace, v.env, v.app, connection, cfg)
		if err != nil {
			return err
		}
		hash := buildInputHash(connection, head, cfg)
		// A rejected target must not roll back admissions for other environments.
		target, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = w.service.insertPipeline(ctx, target, git, a.Workspace, v.env, uuid.New(), runner, pipeline.Command{Action: "build", Connection: connection, CompareCommit: compare, Commit: head, Branch: a.Branch, RevisionID: v.base, UnitID: env.Unit, AppID: v.app, Build: &cfg, InputHash: hash, Registry: registry, SubmittedAt: time.Now().UTC()})
		if err != nil {
			if rollbackErr := target.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			// The runner chosen must be updated before it can build without root, for example,
			// although selectRunner found it could. A fallback chosen is passed over at the next
			// look, so only the primary is waited for.
			if runnerUpgradeRequired(err) && runner != cfg.RunnerID {
				lookAgain = lookAgain || !expired
				continue
			}
			if runnerUpgradeRequired(err) {
				waits, err := waitForRunner(ctx, tx, a.Workspace, v.env, v.app, head, runner, "runner_update", deadline)
				if err != nil {
					return err
				}
				lookAgain = lookAgain || waits
				continue
			}
			reason := ""
			var rejected *buildInputRejection
			if errors.As(err, &rejected) {
				reason = rejected.reason
			} else if runnerUnavailable(err) {
				// Such as a runner that is no longer a build runner.
				reason = "runner_unavailable"
			}
			// Any other refusal, such as its runner upgrading or the workspace's operation limit,
			// passes by itself.
			var refused *domain.Error
			if reason == "" && errors.As(err, &refused) && refused.Code != "invalid_input" {
				lookAgain = true
				continue
			}
			// Each target's runtime-secret check can ask for a search between passes. Asking for
			// them all in one pass keeps the passes a push needs independent of its targets.
			var call *providerCall
			if reason == "" && errors.As(err, &call) {
				unanswered = unanswered.and(call)
				continue
			}
			if reason == "" {
				return err
			}
			if err = rejectTarget(ctx, tx, a.Workspace, v.env, v.app, head, reason); err != nil {
				return err
			}
			continue
		}
		if _, err = target.Exec(ctx, "UPDATE build_targets SET input_hash=$4,requested_commit=$5,automatic_requested_commit=$5,rejected_commit=NULL,rejection_reason=NULL,rejected_at=NULL,waiting_commit=NULL,waiting_reason=NULL,waiting_runner_id=NULL,waiting_since=NULL,waiting_until=NULL WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", a.Workspace, v.env, v.app, hash, head); err != nil {
			return err
		}
		if err = target.Commit(ctx); err != nil {
			return err
		}
		capacity--
	}
	if unanswered != nil {
		return unanswered
	}
	if !waiting && (!lookAgain || expired) {
		// The branch's one job ends, so any wait it recorded, at this commit or an earlier one,
		// ends too: a target it stopped looking at, as when the source it builds on failed or it
		// no longer builds automatically, waits for nothing this job would see.
		if err = db.New(tx).EndBranchBuildWaits(ctx, db.EndBranchBuildWaitsParams{WorkspaceID: a.Workspace, ConnectionID: a.Connection, Branch: a.Branch}); err != nil {
			return err
		}
		if _, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Each look reads the unreadable source again, so its wait comes first even if other work
	// could start sooner: a waiting job would otherwise read it every few seconds.
	if lookAgain && (waiting || !expired) {
		return river.JobSnooze(sourceRetryDelay)
	}
	if waiting {
		return river.JobSnooze(5 * time.Second)
	}
	return nil
}

// refreshBehindBuilds starts a refresh of an imported branch whose automatic builds in that
// environment have not been asked for the commit, as when an earlier refresh held them for an import
// that failed. One already waiting covers it.
func (s *Service) refreshBehindBuilds(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, p pipeline.Command) error {
	behind, err := db.New(tx).AutomaticBuildsBehind(ctx, db.AutomaticBuildsBehindParams{WorkspaceID: ws, EnvironmentID: env, ConnectionID: p.Connection.ID, Branch: p.Branch, Commit: p.Commit})
	if err != nil || !behind {
		return err
	}
	return s.insertGitJob(ctx, tx, workspaceMetadata{Workspace: ws}, refreshArgs{ws, p.Connection.ID, p.Branch}, refreshOpts, false)
}

// refreshLeftBranch starts a refresh of the branch a source no longer imports, when something still
// tracks it there, such as automatic builds an earlier failure of the source held back. now is the
// source's new binding, or nil when it was unbound.
func (s *Service) refreshLeftBranch(ctx context.Context, tx pgx.Tx, ws uuid.UUID, raw []byte, now *pipeline.Source) error {
	var left pipeline.Source
	if err := json.Unmarshal(raw, &left); err != nil {
		return err
	}
	if now != nil && now.ConnectionID == left.ConnectionID && now.Branch == left.Branch {
		return nil
	}
	tracked, err := db.New(tx).GitBranchTracked(ctx, db.GitBranchTrackedParams{WorkspaceID: ws, ConnectionID: left.ConnectionID, Branch: left.Branch})
	if err != nil || !tracked {
		return err
	}
	return s.insertGitJob(ctx, tx, workspaceMetadata{Workspace: ws}, refreshArgs{ws, left.ConnectionID, left.Branch}, refreshOpts, false)
}

func rejectTarget(ctx context.Context, tx pgx.Tx, ws, env, app uuid.UUID, commit, reason string) error {
	return db.New(tx).RejectBuildTarget(ctx, db.RejectBuildTargetParams{Commit: commit, Reason: reason, WorkspaceID: ws, EnvironmentID: env, ApplicationID: app})
}

// Requested commits include cancelled, failed and skipped work; only an actual
// compatible artifact can establish that watched source has already been built.
func successfulBuildBaseline(ctx context.Context, tx pgx.Tx, ws, env, app uuid.UUID, connection pipeline.Connection, cfg pipeline.BuildConfig) (string, error) {
	if len(cfg.WatchPaths) == 0 {
		return "", nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT r.configuration FROM pipeline_runs r JOIN build_artifacts a ON a.id=r.id WHERE r.workspace_id=$1 AND r.environment_id=$2 AND r.application_id=$3 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, ws, env, app).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var previous pipeline.Command
	if err = json.Unmarshal(raw, &previous); err != nil {
		return "", err
	}
	if previous.Build == nil || cfg.AutoDeploy && !previous.Build.AutoDeploy || buildInputHash(connection, previous.Commit, cfg) != previous.InputHash {
		return "", nil
	}
	return previous.Commit, nil
}

// explainImportAgain says why an import of a commit the source held before its latest import
// runs, which job history would otherwise show as an unexplained repeat: the branch moved
// back, as a force-push that removes a commit does.
func explainImportAgain(ctx context.Context, tx pgx.Tx, ws, env, op uuid.UUID, imported, head string) error {
	commits, err := db.New(tx).LatestImportedCommits(ctx, db.LatestImportedCommitsParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil || len(commits) < 2 || commits[0] != imported || commits[1] != head {
		return err
	}
	reason := "Imports commit " + head + " again because the branch moved back to it from commit " + imported + "."
	return db.New(tx).SetPipelineRunReason(ctx, db.SetPipelineRunReasonParams{Reason: reason, WorkspaceID: ws, ID: op})
}
