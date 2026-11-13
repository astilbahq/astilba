package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// runnerEligible reports whether a runner can take work, and whether it is online. build asks also
// for what every build needs of its runner, which insertPipeline checks again of the runner chosen.
func runnerEligible(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, architecture string, controls, railpack, build bool) (bool, error) {
	var role string
	var raw []byte
	var online bool
	err := tx.QueryRow(ctx, "SELECT role,capabilities,COALESCE(last_seen>now()-interval '45 seconds',false) FROM servers WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND credential_hash IS NOT NULL", ws, id).Scan(&role, &raw, &online)
	if err != nil {
		return false, err
	}
	var c api.AgentCapabilities
	if json.Unmarshal(raw, &c) != nil || role == "deployment" || c.Build == nil || !*c.Build || !c.Docker || (architecture != "" && architecture != string(c.Arch)) {
		return false, domain.Invalid("runner")
	}
	if controls && (c.BuildControls == nil || !*c.BuildControls) {
		return false, domain.Invalid("runner.buildControlsUpgradeRequired")
	}
	if railpack && (c.RailpackVersion == nil || *c.RailpackVersion != pipeline.RailpackVersion) {
		return false, domain.Invalid("runner.railpackUpgradeRequired")
	}
	if build && (c.BuildNetwork == nil || !*c.BuildNetwork) {
		return false, domain.Invalid("runner.buildNetworkUpgradeRequired")
	}
	if build && (c.RootlessBuilds == nil || !*c.RootlessBuilds) {
		return false, domain.Invalid("runner.rootlessBuildsUpgradeRequired")
	}
	return online, nil
}

// selectRunner chooses the runner for work: the primary, or its fallback when the primary is
// offline or busy and the fallback is ready. build is whether the work is a build.
func selectRunner(ctx context.Context, tx pgx.Tx, ws, primary uuid.UUID, fallback *uuid.UUID, architecture string, controls, railpack, build bool) (uuid.UUID, error) {
	online, err := runnerEligible(ctx, tx, ws, primary, architecture, controls, railpack, build)
	if err != nil {
		return uuid.Nil, err
	}
	var busy bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state IN ('queued','dispatched','running','needs_attention'))", primary).Scan(&busy); err != nil {
		return uuid.Nil, err
	}
	if fallback != nil && (!online || busy) {
		// A fallback that can't take the work, as when it was revoked or needs an upgrade, isn't
		// ready, so the primary queues it, as it would with no fallback: whether work starts must
		// not depend on how busy the primary is.
		ready, err := runnerEligible(ctx, tx, ws, *fallback, architecture, controls, railpack, build)
		var refused *domain.Error
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refused) {
			return primary, nil
		}
		if err != nil {
			return uuid.Nil, err
		}
		if ready {
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state IN ('queued','dispatched','running','needs_attention'))", *fallback).Scan(&busy); err != nil {
				return uuid.Nil, err
			}
			if !busy {
				return *fallback, nil
			}
		}
	}
	return primary, nil
}

// sourceFilesUnavailable refuses to start an import that could not be checked afterwards.
var sourceFilesUnavailable = &domain.Error{Code: "temporarily_busy", Message: "The Git provider did not answer for the selected files. Check that the connection can read repository contents, then try again."}

func (s *Service) insertPipeline(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env, key, server uuid.UUID, p pipeline.Command) (api.Operation, error) {
	var out api.Operation
	if p.Action == "import" {
		blobs, err := git.sourceBlobs(ctx, tx, p.Connection.ID, p.Commit, p.Source.Paths)
		if err != nil {
			return out, err
		}
		if blobs.Err != nil {
			return out, blobs.failure(sourceFilesUnavailable)
		}
		// A runner would fail the import without saying which file it missed.
		if missing := absentSourceFiles(p.Source.Paths, blobs.Value); len(missing) > 0 {
			return out, missingSourceFiles(p.Commit, missing)
		}
		p.SourceBlobs = blobs.Value
	}
	if p.Build != nil {
		canonical := p.Build.Canonical()
		p.Build = &canonical
		if canonical.UsesRailpack() {
			pins := pipeline.RailpackFor(canonical.Architecture)
			p.Railpack = &pins
		}
	}
	if p.IsBuild() {
		egress, err := serverBuildEgress(ctx, tx, ws, server)
		if err != nil {
			return out, err
		}
		p.BuildEgress = egress
		p.BuildNetwork = pipeline.BuildNetworkV1
		p.RootlessBuild = pipeline.RootlessBuildV1
		var project uuid.UUID
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT e.project_id,s.capabilities FROM environments e JOIN servers s ON s.workspace_id=e.workspace_id AND s.id=$3 WHERE e.workspace_id=$1 AND e.id=$2", ws, env, server).Scan(&project, &raw); err != nil {
			return out, err
		}
		var capabilities api.AgentCapabilities
		// An agent without it would run build code on the host's network.
		if json.Unmarshal(raw, &capabilities) != nil || capabilities.BuildNetwork == nil || !*capabilities.BuildNetwork {
			return out, domain.Invalid("runner.buildNetworkUpgradeRequired")
		}
		// An agent without it would run build code as root, privileged.
		if capabilities.RootlessBuilds == nil || !*capabilities.RootlessBuilds {
			return out, domain.Invalid("runner.rootlessBuildsUpgradeRequired")
		}
		if capabilities.BuildCache != nil && *capabilities.BuildCache && p.Build != nil && p.Build.CanCache() {
			p.CacheScope = pipeline.Hash([]uuid.UUID{ws, project})
			if p.Preview != nil {
				p.CacheScope = pipeline.Hash([]uuid.UUID{ws, env, p.Preview.RequestID})
			}
		}
	}
	if err := p.Validate(); err != nil {
		return out, domain.Invalid("pipeline")
	}
	hydrated := p
	if err := s.populatePipeline(ctx, tx, git, ws, env, &hydrated); err != nil {
		return out, err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='pipeline' AND state NOT IN ('succeeded','failed','cancelled')", ws).Scan(&count); err != nil {
		return out, err
	}
	if count >= 50 {
		return out, domain.Conflict
	}
	var generation int64
	if err := tx.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3 RETURNING execution_generation", ws, env, p.RevisionID).Scan(&generation); err != nil {
		return out, err
	}
	raw, err := stablejson.Marshal(p)
	if err != nil || len(raw) > 150000 {
		return out, domain.Invalid("pipeline")
	}
	id := uuid.New()
	message := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: id, ServerId: server, EnvironmentId: env, ResourceId: p.UnitID, Generation: generation, Kind: "pipeline", Action: "inspect", PlanHash: p.Digest(), Pipeline: &message, Deadline: time.Now().Add(pipelineLifetime(p))}
	out, err = s.insert(ctx, tx, ws, key, nil, c)
	if err != nil {
		return out, err
	}
	// An import admitted for a source ends its failure.
	if p.Action == "import" {
		if err = db.New(tx).ClearGitSourceFailure(ctx, db.ClearGitSourceFailureParams{WorkspaceID: ws, EnvironmentID: env}); err != nil {
			return out, err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,delivery_started) VALUES($1,$2,$3,$4,$5,$6,false)", id, ws, env, p.AppID, p.RevisionID, raw)
	return out, err
}
func pipelineExisting(ctx context.Context, tx pgx.Tx, ws, env, key uuid.UUID, action string) (api.Operation, bool, error) {
	var c []byte
	err := tx.QueryRow(ctx, "SELECT command FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND request_key=$3 AND kind='pipeline'", ws, env, key).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.Operation{}, false, nil
	}
	if err != nil {
		return api.Operation{}, false, err
	}
	var command api.ExecutionCommand
	var p pipeline.Command
	if json.Unmarshal(c, &command) != nil || command.Pipeline == nil || json.Unmarshal(*command.Pipeline, &p) != nil || p.Action != action {
		return api.Operation{}, false, domain.Conflict
	}
	op, err := scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND id=$2", ws, command.Id))
	return op, true, err
}

func existingBuild(ctx context.Context, tx pgx.Tx, ws, pid, env, app, key uuid.UUID) (api.Operation, bool, error) {
	old, exists, err := pipelineExisting(ctx, tx, ws, env, key, "build")
	if err != nil || !exists {
		return old, exists, err
	}
	var matches bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_runs r JOIN environments e ON e.workspace_id=r.workspace_id AND e.id=r.environment_id
 WHERE r.workspace_id=$1 AND e.project_id=$2 AND r.environment_id=$3 AND r.application_id=$4 AND r.id=$5)`, ws, pid, env, app, old.Id).Scan(&matches); err != nil {
		return api.Operation{}, true, err
	}
	if !matches {
		return api.Operation{}, true, domain.NotFound
	}
	return old, true, nil
}

func (s *Service) GitSource(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (map[string]any, error) {
	out := map[string]any{}
	err := s.scopeIn(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx, w store.Workspace) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		// Each statement here sees the latest commit. A submission's outcome and the source's
		// status commit together, so reading the submission first means the status read after it
		// is never older: a succeeded submission always comes with the status it set.
		submission, err := latestSubmission(ctx, tx, ws, env)
		if err != nil {
			return err
		}
		var cfg []byte
		var actor uuid.UUID
		var revision *uuid.UUID
		var commit, pending, status, failedCommit, failureReason string
		var failedAt *time.Time
		var role *string
		var current, approved *int64
		// The approver's role and version explain, in the terms of policyApproval, why imports don't
		// run; policy_approver_holds is what stops them.
		if err := tx.QueryRow(ctx, `SELECT g.configuration,g.policy_actor,g.imported_revision_id,g.imported_commit,g.pending_commit,g.status,g.policy_auth_version,m.role,a.auth_version,g.failed_commit,g.failure_reason,g.failed_at
 FROM git_sources g LEFT JOIN memberships m ON m.workspace_id=g.workspace_id AND m.account_id=g.policy_actor LEFT JOIN accounts a ON a.id=g.policy_actor
 WHERE g.workspace_id=$1 AND g.environment_id=$2`, ws, env).Scan(&cfg, &actor, &revision, &commit, &pending, &status, &approved, &role, &current, &failedCommit, &failureReason, &failedAt); err != nil {
			return err
		}
		baseline, err := sourceBaseline(ctx, tx, ws, env, revision)
		if err != nil {
			return err
		}
		var configuration pipeline.Source
		if err = json.Unmarshal(cfg, &configuration); err != nil {
			return err
		}
		clean := baseline != nil && sameSourceFiles(v.Spec.Compose, baseline)
		if clean {
			configuration = sourceSelection(configuration, v.Spec.Compose)
		}
		approval := policyApproval(role, current, approved, true, status == "approval_required", w.Role)
		var failure *api.GitSourceFailure
		if failedCommit != "" && failedAt != nil {
			failure = &api.GitSourceFailure{Commit: failedCommit, Reason: failureReason, FailedAt: *failedAt}
		}
		out = map[string]any{"clean": clean, "configuration": configuration, "importedRevisionId": revision, "importedCommit": commit, "pendingCommit": pending, "status": status, "approval": approval, "sourceHash": sourceHash(cfg, actor), "submission": submission, "failure": failure}
		return nil
	})
	return out, err
}

// BindSource binds an environment to a Git source, or binds it again with another connection,
// branch, runner or selection, as an owner must when a selected file is renamed or removed
// upstream. Either way it imports the branch's head and makes the source's automatic imports and
// deployments the caller's. Binding again forgets the earlier import, so the draft is compared
// with, and deployments wait for, this one. An environment runs one operation at a time, so
// binding again is refused while one is running.
// Binding again needs the hash returned with the source, so a change made since it was shown, such
// as another owner's binding, is refused rather than overwritten.
func (s *Service) BindSource(ctx context.Context, p store.Principal, ws, pid, env, base uuid.UUID, c pipeline.Source, hash string) (api.Operation, error) {
	var out api.Operation
	if err := c.Validate(); err != nil {
		return api.Operation{}, domain.Invalid("source")
	}
	// The audit record says whether the source was bound again, which also made it the caller's.
	// Only the pass that commits keeps it.
	err := s.gitScope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "", env, func(tx pgx.Tx, git *gitAnswers) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		// A retired environment's sources stay as they were.
		if err = requireActive(v.State); err != nil {
			return err
		}
		if v.Revision != base || v.Spec.Compose == nil {
			return domain.Invalid("source")
		}
		// A push that finds no source tracking its branch starts no refresh.
		if err = db.New(tx).ShareGitConnectionDeliveries(ctx, db.ShareGitConnectionDeliveriesParams{WorkspaceID: ws, ID: c.ConnectionID}); err != nil {
			return err
		}
		head, err := git.head(ctx, tx, c.ConnectionID, c.Branch)
		if err != nil {
			return err
		}
		if head.Err != nil {
			return head.failure(domain.Invalid("branch"))
		}
		connection, commit := head.Connection, head.Value
		server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, "", false, false, false)
		if err != nil {
			return err
		}
		action := "git.source.rebind"
		bound, err := db.New(tx).LockGitSourcePolicy(ctx, db.LockGitSourcePolicyParams{WorkspaceID: ws, EnvironmentID: env})
		switch {
		case errors.Is(err, pgx.ErrNoRows) && hash != "":
			// A hash names a source that was shown; one that is gone since was removed.
			return sourceChangedSinceShown
		case errors.Is(err, pgx.ErrNoRows):
			action = "git.source.bind"
		case err != nil:
			return err
		case sourceHash(bound.Configuration, bound.PolicyActor) != hash:
			return sourceChangedSinceShown
		}
		running, err := db.New(tx).EnvironmentOperationRunning(ctx, db.EnvironmentOperationRunningParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		if running {
			return sourceImporting
		}
		if err = store.Audit(ctx, db.New(tx), p, ws, action, env); err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(c)
		if err = db.New(tx).BindGitSource(ctx, db.BindGitSourceParams{WorkspaceID: ws, EnvironmentID: env, ConnectionID: c.ConnectionID, Configuration: raw, PolicyActor: p.Account.ID, PolicyAuthVersion: p.Account.AuthVersion}); err != nil {
			return err
		}
		if action == "git.source.rebind" {
			if err = s.refreshLeftBranch(ctx, tx, ws, bound.Configuration, &c); err != nil {
				return err
			}
		}
		command := pipeline.Command{Action: "import", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, Source: &c, SubmittedAt: time.Now().UTC()}
		out, err = s.insertPipeline(ctx, tx, git, ws, env, uuid.New(), server, command)
		return err
	})
	return operationFor(p, out), err
}
func (s *Service) RefreshSource(ctx context.Context, p store.Principal, ws, pid, env, base uuid.UUID, discard bool) (api.Operation, error) {
	var out api.Operation
	err := s.gitScope(ctx, p, ws, workspaceAccess{Write: true}, "git.source.refresh", env, func(tx pgx.Tx, git *gitAnswers) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != base {
			return domain.Conflict
		}
		var raw []byte
		var imported *uuid.UUID
		if err = tx.QueryRow(ctx, "SELECT configuration,imported_revision_id FROM git_sources WHERE workspace_id=$1 AND environment_id=$2 FOR UPDATE", ws, env).Scan(&raw, &imported); err != nil {
			return err
		}
		baseline, err := sourceBaseline(ctx, tx, ws, env, imported)
		if err != nil {
			return err
		}
		clean := imported == nil || sameSourceFiles(v.Spec.Compose, baseline)
		var c pipeline.Source
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		head, err := git.head(ctx, tx, c.ConnectionID, c.Branch)
		if err != nil {
			return err
		}
		if head.Err != nil {
			return head.failure(domain.Invalid("branch"))
		}
		connection, commit := head.Connection, head.Value
		if clean && imported != nil {
			c = sourceSelection(c, v.Spec.Compose)
		}
		if !clean && !discard {
			var merged bool
			c, merged, err = mergedSourceSelection(ctx, tx, git, ws, env, v.Spec.Compose, commit, c)
			var unanswered *reviewUnanswered
			if errors.As(err, &unanswered) && unanswered.gone() {
				return &domain.Error{Code: "conflict", Message: reviewGoneReason}
			}
			if err != nil {
				return err
			}
			if !merged {
				return domain.Conflict
			}
		}
		server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, "", false, false, false)
		if err != nil {
			return err
		}
		out, err = s.insertPipeline(ctx, tx, git, ws, env, uuid.New(), server, pipeline.Command{Action: "import", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, Source: &c, SubmittedAt: time.Now().UTC()})
		return err
	})
	return operationFor(p, out), err
}

// UnbindSource stops importing an environment from Git. The draft keeps the Compose files it has,
// and an import still running saves nothing when it reports.
func (s *Service) UnbindSource(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.source.unbind", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		// A retired environment's sources stay as they were.
		if err = requireActive(v.State); err != nil {
			return err
		}
		bound, err := db.New(tx).LockGitSourcePolicy(ctx, db.LockGitSourcePolicyParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		if _, err = db.New(tx).UnbindGitSource(ctx, db.UnbindGitSourceParams{WorkspaceID: ws, EnvironmentID: env}); err != nil {
			return err
		}
		return s.refreshLeftBranch(ctx, tx, ws, bound.Configuration, nil)
	})
}
func (s *Service) CreateBuild(ctx context.Context, p store.Principal, ws, pid, env, base, app, key uuid.UUID, c pipeline.BuildConfig) (api.Operation, error) {
	var out api.Operation
	if p.Automation != nil {
		return api.Operation{}, domain.Forbidden
	}
	c = c.Canonical()
	if err := c.Validate(); err != nil {
		var validation *pipeline.BuildValidationError
		if errors.As(err, &validation) {
			return api.Operation{}, &domain.Error{Code: "invalid_input", Message: validation.Message, Fields: []string{"build." + validation.Field}}
		}
		return api.Operation{}, domain.Invalid("build")
	}
	err := s.gitScope(ctx, p, ws, workspaceAccess{Write: true, Owner: c.AutoDeploy, Recent: c.AutoDeploy}, "build.create", app, func(tx pgx.Tx, git *gitAnswers) error {
		var err error
		out, err = s.admitBuild(ctx, tx, git, p, ws, pid, env, base, app, key, c, true)
		return err
	})
	return operationFor(p, out), err
}

func (s *Service) admitBuild(ctx context.Context, tx pgx.Tx, git *gitAnswers, p store.Principal, ws, pid, env, base, app, key uuid.UUID, c pipeline.BuildConfig, persist bool) (api.Operation, error) {
	if persist {
		if old, exists, err := existingBuild(ctx, tx, ws, pid, env, app, key); err != nil || exists {
			return old, err
		}
	}
	v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
	if err != nil {
		return api.Operation{}, err
	}
	if v.Revision != base {
		return api.Operation{}, domain.Conflict
	}
	found := false
	for _, a := range v.Spec.Applications {
		found = found || a.ID == app
	}
	if !found {
		return api.Operation{}, domain.Invalid("applicationId")
	}
	// A push that finds no target tracking its branch starts no refresh.
	if persist {
		if err = db.New(tx).ShareGitConnectionDeliveries(ctx, db.ShareGitConnectionDeliveriesParams{WorkspaceID: ws, ID: c.ConnectionID}); err != nil {
			return api.Operation{}, err
		}
	}
	head, err := git.head(ctx, tx, c.ConnectionID, c.Branch)
	if err != nil {
		return api.Operation{}, err
	}
	if head.Err != nil {
		return api.Operation{}, head.failure(domain.Invalid("branch"))
	}
	connection, commit := head.Connection, head.Value
	server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, c.Architecture, c.NeedsControls(), c.UsesRailpack(), true)
	if err != nil {
		return api.Operation{}, err
	}
	var registry *pipeline.Registry
	if c.RegistryID != nil {
		r, _, err := loadRegistry(ctx, tx, ws, *c.RegistryID)
		if err != nil {
			return api.Operation{}, err
		}
		registry = &r
	}
	hash := buildInputHash(connection, commit, c)
	out, err := s.insertPipeline(ctx, tx, git, ws, env, key, server, pipeline.Command{Action: "build", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, AppID: app, Build: &c, InputHash: hash, Registry: registry, SubmittedAt: time.Now().UTC()})
	if err != nil {
		return out, err
	}
	if persist {
		raw, _ := stablejson.Marshal(c)
		_, err = tx.Exec(ctx, "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,automatic_requested_commit,policy_actor,policy_auth_version,policy_owner) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,policy_approver_holds($1,$8,$9,true)) ON CONFLICT(workspace_id,environment_id,application_id) DO UPDATE SET connection_id=EXCLUDED.connection_id,configuration=EXCLUDED.configuration,input_hash=EXCLUDED.input_hash,requested_commit=EXCLUDED.requested_commit,automatic_requested_commit=EXCLUDED.automatic_requested_commit,policy_actor=EXCLUDED.policy_actor,policy_auth_version=EXCLUDED.policy_auth_version,policy_owner=EXCLUDED.policy_owner,rejected_commit=NULL,rejection_reason=NULL,rejected_at=NULL", ws, env, app, c.ConnectionID, raw, hash, commit, p.Account.ID, p.Account.AuthVersion)
	} else {
		_, err = tx.Exec(ctx, "UPDATE build_targets SET input_hash=$4,requested_commit=$5,rejected_commit=NULL,rejection_reason=NULL,rejected_at=NULL WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", ws, env, app, hash, commit)
	}
	if err != nil {
		return api.Operation{}, err
	}
	return out, nil
}
func buildInputHash(c pipeline.Connection, commit string, b pipeline.BuildConfig) string {
	// Scheduling and deployment policy do not change the built artifact.
	b.RunnerID = uuid.Nil
	b.FallbackRunnerID = nil
	b.RegistryID = nil
	b.AutoBuild = false
	b.AutoDeploy = false
	b.Budget = nil
	b.WatchPaths = nil
	b = b.Canonical()
	if b.UsesRailpack() {
		return pipeline.Hash(struct {
			Connection      uuid.UUID
			Commit, Builder string
			Inputs          pipeline.BuildConfig
			Railpack        pipeline.RailpackInputs
		}{c.ID, commit, pipeline.BuilderImage, b, pipeline.RailpackFor(b.Architecture)})
	}
	return pipeline.Hash(struct {
		Connection      uuid.UUID
		Commit, Builder string
		Inputs          pipeline.BuildConfig
	}{c.ID, commit, pipeline.BuilderImage, b})
}
func (s *Service) PipelineRuns(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.id,r.configuration,r.result,r.created_at,o.server_id,o.environment_id,o.generation,o.state,o.kind,o.cancel_requested,o.note,o.steps,a.identity,d.state,d.note,d.operation_id
  FROM pipeline_runs r JOIN execution_operations o ON o.id=r.id
  LEFT JOIN build_artifacts a ON a.id=r.id
  LEFT JOIN pipeline_auto_deployments d ON d.id=r.id
  WHERE r.workspace_id=$1 AND r.environment_id=$2 ORDER BY r.created_at DESC LIMIT 100`, ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var op api.Operation
			var raw, result, steps, artifact []byte
			var created time.Time
			var autoState, autoNote *string
			var autoOperation *uuid.UUID
			if err = rows.Scan(&op.Id, &raw, &result, &created, &op.ServerId, &op.EnvironmentId, &op.Generation, &op.State, &op.Kind, &op.CancelRequested, &op.Note, &steps, &artifact, &autoState, &autoNote, &autoOperation); err != nil {
				return err
			}
			op.CreatedAt = created
			if err = json.Unmarshal(steps, &op.Steps); err != nil {
				return err
			}
			var c pipeline.Command
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			data := map[string]any{"id": op.Id, "applicationId": c.AppID, "action": c.Action, "commit": c.Commit, "inputHash": c.InputHash, "operation": op, "createdAt": created}
			if c.Build != nil {
				data["configuration"] = c.Build
			}
			if len(result) > 0 {
				data["result"] = jsontext.Value(result)
			}
			if len(artifact) > 0 {
				data["artifact"] = jsontext.Value(artifact)
			}
			if autoState != nil {
				data["automaticDeployment"] = map[string]any{"state": *autoState, "note": *autoNote, "operationId": autoOperation}
			}
			out = append(out, data)
		}
		return rows.Err()
	})
	return out, err
}

func pipelineLifetime(p pipeline.Command) time.Duration {
	if p.IsBuild() && p.Build != nil {
		return time.Duration(p.Build.Limits().Minutes) * time.Minute
	}
	return 30 * time.Minute
}
