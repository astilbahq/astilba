package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
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

func (refreshArgs) Kind() string { return "platform_git_refresh_v1" }
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
	_, err := s.Queue.InsertTx(ctx, tx, refreshArgs{ws, id, p.Branch}, &river.InsertOpts{MaxAttempts: 100, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}})
	return err
}

type refreshWorker struct {
	river.WorkerDefaults[refreshArgs]
	service *Service
}

func (w *refreshWorker) Work(ctx context.Context, job *river.Job[refreshArgs]) error {
	return w.service.withGit(ctx, job.Args.Workspace, func(git *gitAnswers) error { return w.work(ctx, git, job) })
}

// A later push for the branch joins this job rather than starting another, so a source waiting
// to be read looks again often, and gives up in time for a new refresh to take over.
const (
	sourceRetryDelay = time.Minute
	sourceWaitLimit  = 24 * time.Hour
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
			return nil
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
	if resolved.Err != nil {
		return resolved.Err
	}
	connection, head := resolved.Connection, resolved.Value
	rows, err := tx.Query(ctx, `SELECT g.environment_id,g.configuration,g.imported_revision_id,g.imported_commit,e.project_id,e.draft_revision_id,g.policy_actor,g.pending_commit
 FROM git_sources g JOIN environments e ON e.workspace_id=g.workspace_id AND e.id=g.environment_id
 JOIN memberships m ON m.workspace_id=g.workspace_id AND m.account_id=g.policy_actor
 WHERE g.workspace_id=$1 AND g.connection_id=$2 AND g.status<>'approval_required' AND g.configuration->>'branch'=$3 AND m.role IN ('owner','operator')
 ORDER BY g.pending_commit=$4,g.environment_id`, a.Workspace, a.Connection, a.Branch, head)
	if err != nil {
		return err
	}
	type source struct {
		env, pid, base, actor uuid.UUID
		imported              *uuid.UUID
		raw                   []byte
		commit, pending       string
	}
	sources := []source{}
	for rows.Next() {
		var v source
		if err = rows.Scan(&v.env, &v.raw, &v.imported, &v.commit, &v.pid, &v.base, &v.actor, &v.pending); err != nil {
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
	// A source whose files cannot be read waits for this job to look again. Work elsewhere starts now,
	// but its environment's builds wait for it, because they build on the imported draft.
	var unreadable error
	waitingToImport := map[uuid.UUID]bool{}
	// Past its limit the job stops reading a source already shown as waiting, whatever else keeps
	// it alive, and shows any source it did not reach before it ends.
	expired := time.Since(job.CreatedAt) > sourceWaitLimit
	for _, v := range sources {
		if v.commit == head {
			if _, err = tx.Exec(ctx, "UPDATE git_sources SET pending_commit='' WHERE workspace_id=$1 AND environment_id=$2 AND status<>'conflict' AND pending_commit<>''", a.Workspace, v.env); err != nil {
				return err
			}
			continue
		}
		// A retry or wait must not repeat an import this job already started, whatever became of
		// it, and needs no more of the provider's time for this source. Only the job's latest
		// counts, so a branch moved back to an earlier commit imports that commit again.
		var started bool
		if err = tx.QueryRow(ctx, "SELECT COALESCE((SELECT configuration->>'commit' FROM pipeline_runs WHERE workspace_id=$1 AND environment_id=$2 AND configuration->>'action'='import' AND created_at>=$4 ORDER BY created_at DESC LIMIT 1)=$3,false)", a.Workspace, v.env, head, job.CreatedAt).Scan(&started); err != nil {
			return err
		}
		if started {
			continue
		}
		if capacity <= 0 {
			waiting = true
			continue
		}
		var cfg pipeline.Source
		if err = json.Unmarshal(v.raw, &cfg); err != nil {
			return err
		}
		busy, err := db.New(tx).EnvironmentHoldsAutomaticWork(ctx, db.EnvironmentHoldsAutomaticWorkParams{WorkspaceID: a.Workspace, EnvironmentID: v.env})
		if err != nil {
			return err
		}
		if busy {
			waiting = true
			continue
		}
		env, err := loadVisualEnvironment(ctx, tx, a.Workspace, v.pid, v.env)
		if err != nil {
			return err
		}
		baseline, err := sourceBaseline(ctx, tx, a.Workspace, v.env, v.imported)
		if err != nil {
			return err
		}
		clean := v.imported == nil || sameSourceFiles(env.Spec.Compose, baseline)
		if clean && v.imported != nil {
			cfg = sourceSelection(cfg, env.Spec.Compose)
		}
		if !clean {
			cfg, clean, err = mergedSourceSelection(ctx, tx, git, a.Workspace, v.env, env.Spec.Compose, head, cfg)
			var call *providerCall
			if errors.As(err, &call) {
				unanswered = unanswered.and(call)
				continue
			}
			if err != nil {
				return err
			}
		}
		if !clean {
			if _, err = tx.Exec(ctx, "UPDATE git_sources SET pending_commit=$3,status='conflict' WHERE workspace_id=$1 AND environment_id=$2", a.Workspace, v.env, head); err != nil {
				return err
			}
			continue
		}
		if expired && v.pending == head {
			waitingToImport[v.env] = true
			continue
		}
		blobs, err := git.sourceBlobs(ctx, tx, a.Connection, head, cfg.Paths)
		var call *providerCall
		if errors.As(err, &call) {
			unanswered = unanswered.and(call)
			continue
		}
		if err != nil {
			return err
		}
		if blobs.Err != nil {
			unreadable = blobs.Err
			waitingToImport[v.env] = true
			if errors.Is(blobs.Err, errSourceBlobsNotAsked) && !expired {
				continue
			}
			if _, err = tx.Exec(ctx, "UPDATE git_sources SET pending_commit=$3 WHERE workspace_id=$1 AND environment_id=$2", a.Workspace, v.env, head); err != nil {
				return err
			}
			continue
		}
		runner, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, "", false, false)
		if err != nil {
			return err
		}
		_, err = w.service.insertPipeline(ctx, tx, git, a.Workspace, v.env, uuid.New(), runner, pipeline.Command{Action: "import", Connection: connection, Commit: head, Branch: a.Branch, RevisionID: v.base, UnitID: env.Unit, Source: &cfg, SubmittedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE git_sources SET pending_commit='' WHERE workspace_id=$1 AND environment_id=$2 AND status<>'conflict'", a.Workspace, v.env); err != nil {
			return err
		}
		capacity--
	}
	if unanswered != nil {
		return unanswered
	}
	// Each target compares against current provider state, so delivery order cannot roll it backwards.
	rows, err = tx.Query(ctx, `SELECT b.environment_id,b.application_id,b.configuration,e.project_id,e.draft_revision_id,b.automatic_requested_commit
 FROM build_targets b JOIN environments e ON e.workspace_id=b.workspace_id AND e.id=b.environment_id
 JOIN memberships m ON m.workspace_id=b.workspace_id AND m.account_id=b.policy_actor
 WHERE b.workspace_id=$1 AND b.connection_id=$2 AND b.configuration->>'branch'=$3 AND b.configuration->>'autoBuild'='true' AND m.role IN ('owner','operator')`, a.Workspace, a.Connection, a.Branch)
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
		runner, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, cfg.Architecture, cfg.NeedsControls(), cfg.UsesRailpack())
		if err != nil {
			return err
		}
		var registry *pipeline.Registry
		if cfg.RegistryID != nil {
			r, _, err := loadRegistry(ctx, tx, a.Workspace, *cfg.RegistryID)
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
			var rejected *buildInputRejection
			if errors.As(err, &rejected) {
				if _, err = tx.Exec(ctx, "UPDATE build_targets SET rejected_commit=$4,rejection_reason=$5,rejected_at=clock_timestamp() WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", a.Workspace, v.env, v.app, head, rejected.reason); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if _, err = target.Exec(ctx, "UPDATE build_targets SET input_hash=$4,requested_commit=$5,automatic_requested_commit=$5,rejected_commit=NULL,rejection_reason=NULL,rejected_at=NULL WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", a.Workspace, v.env, v.app, hash, head); err != nil {
			return err
		}
		if err = target.Commit(ctx); err != nil {
			return err
		}
		capacity--
	}
	if !waiting && (unreadable == nil || expired) {
		if _, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Each look reads the unreadable source again, so its wait comes first even if other work
	// could start sooner: a waiting job would otherwise read it every few seconds.
	if unreadable != nil && (waiting || !expired) {
		return river.JobSnooze(sourceRetryDelay)
	}
	if waiting {
		return river.JobSnooze(5 * time.Second)
	}
	return nil
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
