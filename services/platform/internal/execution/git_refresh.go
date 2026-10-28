package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
	_, err := s.Queue.InsertTx(ctx, tx, refreshArgs{ws, id, p.Branch}, &river.InsertOpts{MaxAttempts: 100, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}})
	return err
}

type refreshWorker struct {
	river.WorkerDefaults[refreshArgs]
	service *Service
}

func (w *refreshWorker) Work(ctx context.Context, job *river.Job[refreshArgs]) error {
	tx, err := w.service.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a := job.Args
	// Match interactive mutation's workspace lock before inspecting membership and drafts.
	if _, err = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", a.Workspace); err != nil {
		return err
	}
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
	provider, connection, err := w.service.provider(ctx, tx, a.Workspace, a.Connection)
	if err != nil {
		return err
	}
	head, err := provider.Resolve(ctx, a.Branch)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT g.environment_id,g.configuration,g.imported_revision_id,g.imported_commit,e.project_id,e.draft_revision_id,g.policy_actor
 FROM git_sources g JOIN environments e ON e.workspace_id=g.workspace_id AND e.id=g.environment_id
 JOIN memberships m ON m.workspace_id=g.workspace_id AND m.account_id=g.policy_actor
 WHERE g.workspace_id=$1 AND g.connection_id=$2 AND g.status<>'approval_required' AND g.configuration->>'branch'=$3 AND m.role IN ('owner','operator')`, a.Workspace, a.Connection, a.Branch)
	if err != nil {
		return err
	}
	type source struct {
		env, pid, base, actor uuid.UUID
		imported              *uuid.UUID
		raw                   []byte
		commit                string
	}
	sources := []source{}
	for rows.Next() {
		var v source
		if err = rows.Scan(&v.env, &v.raw, &v.imported, &v.commit, &v.pid, &v.base, &v.actor); err != nil {
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
	for _, v := range sources {
		if v.commit == head {
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
		var busy bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND state NOT IN ('succeeded','failed','cancelled'))", a.Workspace, v.env).Scan(&busy); err != nil {
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
			cfg, clean, err = mergedSourceSelection(ctx, tx, a.Workspace, v.env, env.Spec.Compose, provider, head, cfg)
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
		runner, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, "", false, false)
		if err != nil {
			return err
		}
		_, err = w.service.insertPipeline(ctx, tx, a.Workspace, v.env, uuid.New(), runner, pipeline.Command{Action: "import", Connection: connection, Commit: head, Branch: a.Branch, RevisionID: v.base, UnitID: env.Unit, Source: &cfg, SubmittedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		capacity--
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
	var admissionErr error
	for _, v := range targets {
		if v.commit == head {
			continue
		}
		if capacity <= 0 {
			waiting = true
			continue
		}
		var busy bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE environment_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", v.env).Scan(&busy); err != nil {
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
		_, err = w.service.insertPipeline(ctx, target, a.Workspace, v.env, uuid.New(), runner, pipeline.Command{Action: "build", Connection: connection, CompareCommit: compare, Commit: head, Branch: a.Branch, RevisionID: v.base, UnitID: env.Unit, AppID: v.app, Build: &cfg, InputHash: hash, Registry: registry, SubmittedAt: time.Now().UTC()})
		if err != nil {
			if rollbackErr := target.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			var invalid *domain.Error
			if errors.As(err, &invalid) && invalid.Code == "invalid_input" {
				admissionErr = err
				continue
			}
			return err
		}
		if _, err = target.Exec(ctx, "UPDATE build_targets SET input_hash=$4,requested_commit=$5,automatic_requested_commit=$5 WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", a.Workspace, v.env, v.app, hash, head); err != nil {
			return err
		}
		if err = target.Commit(ctx); err != nil {
			return err
		}
		capacity--
	}
	if !waiting && admissionErr == nil {
		if _, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if admissionErr != nil {
		return admissionErr
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
