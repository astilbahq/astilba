package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func runnerEligible(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, architecture string, controls, railpack bool) (bool, error) {
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
	return online, nil
}
func selectRunner(ctx context.Context, tx pgx.Tx, ws, primary uuid.UUID, fallback *uuid.UUID, architecture string, controls, railpack bool) (uuid.UUID, error) {
	online, err := runnerEligible(ctx, tx, ws, primary, architecture, controls, railpack)
	if err != nil {
		return uuid.Nil, err
	}
	var busy bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", primary).Scan(&busy); err != nil {
		return uuid.Nil, err
	}
	if fallback != nil && (!online || busy) {
		ready, err := runnerEligible(ctx, tx, ws, *fallback, architecture, controls, railpack)
		if err != nil {
			return uuid.Nil, err
		}
		if ready {
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", *fallback).Scan(&busy); err != nil {
				return uuid.Nil, err
			}
			if !busy {
				return *fallback, nil
			}
		}
	}
	return primary, nil
}
func (s *Service) insertPipeline(ctx context.Context, tx pgx.Tx, ws, env, key, server uuid.UUID, p pipeline.Command) (api.Operation, error) {
	var out api.Operation
	if p.Build != nil {
		canonical := p.Build.Canonical()
		p.Build = &canonical
		if canonical.UsesRailpack() {
			pins := pipeline.RailpackFor(canonical.Architecture)
			p.Railpack = &pins
		}
	}
	if p.IsBuild() {
		var project uuid.UUID
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT e.project_id,s.capabilities FROM environments e JOIN servers s ON s.workspace_id=e.workspace_id AND s.id=$3 WHERE e.workspace_id=$1 AND e.id=$2", ws, env, server).Scan(&project, &raw); err != nil {
			return out, err
		}
		var capabilities api.AgentCapabilities
		if json.Unmarshal(raw, &capabilities) == nil && capabilities.BuildCache != nil && *capabilities.BuildCache && p.Build != nil && p.Build.CanCache() {
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
	if err := s.populatePipeline(ctx, tx, ws, &hydrated); err != nil {
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
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 150000 {
		return out, domain.Invalid("pipeline")
	}
	id := uuid.New()
	message := json.RawMessage(raw)
	c := api.ExecutionCommand{Id: id, ServerId: server, EnvironmentId: env, ResourceId: p.UnitID, Generation: generation, Kind: "pipeline", Action: "inspect", PlanHash: p.Digest(), Pipeline: &message, Deadline: time.Now().Add(pipelineLifetime(p))}
	out, err = s.insert(ctx, tx, ws, key, nil, c)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration) VALUES($1,$2,$3,$4,$5,$6)", id, ws, env, p.AppID, p.RevisionID, raw)
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
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		var cfg []byte
		var revision *uuid.UUID
		var commit, pending, status string
		if err := tx.QueryRow(ctx, "SELECT configuration,imported_revision_id,imported_commit,pending_commit,status FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&cfg, &revision, &commit, &pending, &status); err != nil {
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
		out = map[string]any{"clean": clean, "configuration": configuration, "importedRevisionId": revision, "importedCommit": commit, "pendingCommit": pending, "status": status}
		return nil
	})
	return out, err
}
func (s *Service) BindSource(ctx context.Context, p store.Principal, ws, pid, env, base uuid.UUID, c pipeline.Source) (api.Operation, error) {
	var out api.Operation
	if err := c.Validate(); err != nil {
		return out, domain.Invalid("source")
	}
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.source.bind", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != base || v.Spec.Compose == nil {
			return domain.Invalid("source")
		}
		provider, connection, err := s.provider(ctx, tx, ws, c.ConnectionID)
		if err != nil {
			return err
		}
		commit, err := provider.Resolve(ctx, c.Branch)
		if err != nil {
			return domain.Invalid("branch")
		}
		server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, "", false, false)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(c)
		_, err = tx.Exec(ctx, "INSERT INTO git_sources(workspace_id,environment_id,connection_id,configuration,policy_actor) VALUES($1,$2,$3,$4,$5)", ws, env, c.ConnectionID, raw, p.Account.ID)
		if err != nil {
			return err
		}
		command := pipeline.Command{Action: "import", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, Source: &c, SubmittedAt: time.Now().UTC()}
		out, err = s.insertPipeline(ctx, tx, ws, env, uuid.New(), server, command)
		return err
	})
	return out, err
}
func (s *Service) RefreshSource(ctx context.Context, p store.Principal, ws, pid, env, base uuid.UUID, discard bool) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true}, "git.source.refresh", env, func(tx pgx.Tx) error {
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
		provider, connection, err := s.provider(ctx, tx, ws, c.ConnectionID)
		if err != nil {
			return err
		}
		commit, err := provider.Resolve(ctx, c.Branch)
		if err != nil {
			return domain.Invalid("branch")
		}
		if clean && imported != nil {
			c = sourceSelection(c, v.Spec.Compose)
		}
		if !clean && !discard {
			var merged bool
			c, merged, err = mergedSourceSelection(ctx, tx, ws, env, v.Spec.Compose, provider, commit, c)
			if err != nil {
				return err
			}
			if !merged {
				return domain.Conflict
			}
		}
		server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, "", false, false)
		if err != nil {
			return err
		}
		out, err = s.insertPipeline(ctx, tx, ws, env, uuid.New(), server, pipeline.Command{Action: "import", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, Source: &c, SubmittedAt: time.Now().UTC()})
		return err
	})
	return out, err
}
func (s *Service) SubmitSource(ctx context.Context, p store.Principal, ws, pid, env, base, key uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true}, "git.source.submit", base, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != base || v.Spec.Compose == nil {
			return domain.Conflict
		}
		if old, exists, err := pipelineExisting(ctx, tx, ws, env, key, "submit"); err != nil || exists {
			out = old
			return err
		}
		var source pipeline.Source
		var raw []byte
		var imported *uuid.UUID
		var commit string
		if err = tx.QueryRow(ctx, "SELECT configuration,imported_revision_id,imported_commit FROM git_sources WHERE workspace_id=$1 AND environment_id=$2 FOR UPDATE", ws, env).Scan(&raw, &imported, &commit); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &source); err != nil {
			return err
		}
		baseline, err := sourceBaseline(ctx, tx, ws, env, imported)
		if err != nil {
			return err
		}
		if imported == nil || sameSourceFiles(v.Spec.Compose, baseline) || v.Spec.Compose.Origin == nil || v.Spec.Compose.Origin.Commit != commit {
			return domain.Invalid("source.draft")
		}
		var existing uuid.UUID
		var prior pipeline.Command
		err = tx.QueryRow(ctx, "SELECT operation_id FROM git_submissions WHERE workspace_id=$1 AND environment_id=$2 AND revision_id=$3", ws, env, base).Scan(&existing)
		if err == nil {
			out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND id=$2", ws, existing))
			if err != nil {
				return err
			}
			if out.State != "failed" && out.State != "cancelled" {
				return nil
			}
			var frozen []byte
			if err = tx.QueryRow(ctx, "SELECT configuration FROM pipeline_runs WHERE workspace_id=$1 AND id=$2", ws, existing).Scan(&frozen); err != nil {
				return err
			}
			if err = json.Unmarshal(frozen, &prior); err != nil {
				return err
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		provider, connection, err := s.provider(ctx, tx, ws, source.ConnectionID)
		if err != nil {
			return err
		}
		head, err := provider.Resolve(ctx, source.Branch)
		if err != nil {
			return domain.Invalid("source.branch")
		}
		if head != commit {
			return domain.Conflict
		}
		if err = s.rejectRuntimeSecrets(ctx, tx, ws, env, composeproject.Bundle{Files: append(append([]composeproject.File{}, baseline.Files...), v.Spec.Compose.Files...)}); err != nil {
			return err
		}
		server, err := selectRunner(ctx, tx, ws, source.RunnerID, source.FallbackRunnerID, "", false, false)
		if err != nil {
			return err
		}
		submittedAt := time.Now().UTC()
		if !prior.SubmittedAt.IsZero() {
			submittedAt = prior.SubmittedAt
		}
		out, err = s.insertPipeline(ctx, tx, ws, env, key, server, pipeline.Command{SubmissionID: uuid.NewSHA1(env, []byte("source/"+base.String())), Action: "submit", Connection: connection, Commit: commit, Branch: source.Branch, RevisionID: base, UnitID: v.Unit, Bundle: v.Spec.Compose, Source: &source, SubmittedAt: submittedAt})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO git_submissions(workspace_id,environment_id,revision_id,operation_id,request_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,environment_id,revision_id) DO UPDATE SET operation_id=EXCLUDED.operation_id,request_key=EXCLUDED.request_key", ws, env, base, out.Id, key)
		return err
	})
	return out, err
}
func (s *Service) CreateBuild(ctx context.Context, p store.Principal, ws, pid, env, base, app, key uuid.UUID, c pipeline.BuildConfig) (api.Operation, error) {
	var out api.Operation
	if p.Automation != nil {
		return out, domain.Forbidden
	}
	c = c.Canonical()
	if err := c.Validate(); err != nil {
		var validation *pipeline.BuildValidationError
		if errors.As(err, &validation) {
			return out, &domain.Error{Code: "invalid_input", Message: validation.Message, Fields: []string{"build." + validation.Field}}
		}
		return out, domain.Invalid("build")
	}
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: c.AutoDeploy, Recent: c.AutoDeploy}, "build.create", app, func(tx pgx.Tx) error {
		var err error
		out, err = s.admitBuild(ctx, tx, p, ws, pid, env, base, app, key, c, true)
		return err
	})
	return out, err
}

func (s *Service) admitBuild(ctx context.Context, tx pgx.Tx, p store.Principal, ws, pid, env, base, app, key uuid.UUID, c pipeline.BuildConfig, persist bool) (api.Operation, error) {
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
	provider, connection, err := s.provider(ctx, tx, ws, c.ConnectionID)
	if err != nil {
		return api.Operation{}, err
	}
	commit, err := provider.Resolve(ctx, c.Branch)
	if err != nil {
		return api.Operation{}, domain.Invalid("branch")
	}
	server, err := selectRunner(ctx, tx, ws, c.RunnerID, c.FallbackRunnerID, c.Architecture, c.NeedsControls(), c.UsesRailpack())
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
	total := 0
	for _, id := range c.Secrets {
		var cipher []byte
		if err = tx.QueryRow(ctx, "SELECT encrypted_value FROM build_secret_versions WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&cipher); err != nil {
			return api.Operation{}, domain.Invalid("build.secrets")
		}
		plain, err := s.Store.Keys.Open(cipher, integrationIdentity(ws, id, "build-secret"))
		if err != nil {
			return api.Operation{}, err
		}
		total += len(plain)
		for _, v := range c.Arguments {
			if len(plain) > 0 && strings.Contains(v, string(plain)) {
				return api.Operation{}, domain.Invalid("build.argumentsContainSecret")
			}
		}
	}
	argumentFiles := []composeproject.File{}
	for _, value := range c.Arguments {
		argumentFiles = append(argumentFiles, composeproject.File{Content: value})
	}
	if len(c.Arguments) > 0 {
		if err = s.rejectRuntimeSecrets(ctx, tx, ws, env, composeproject.Bundle{Files: argumentFiles}); err != nil {
			return api.Operation{}, err
		}
	}
	if total > 32768 {
		return api.Operation{}, domain.Invalid("build.secrets")
	}
	hash := buildInputHash(connection, commit, c)
	if persist {
		raw, _ := json.Marshal(c)
		_, err = tx.Exec(ctx, "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,automatic_requested_commit,policy_actor) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8) ON CONFLICT(workspace_id,environment_id,application_id) DO UPDATE SET connection_id=EXCLUDED.connection_id,configuration=EXCLUDED.configuration,input_hash=EXCLUDED.input_hash,requested_commit=EXCLUDED.requested_commit,automatic_requested_commit=EXCLUDED.automatic_requested_commit,policy_actor=EXCLUDED.policy_actor", ws, env, app, c.ConnectionID, raw, hash, commit, p.Account.ID)
	} else {
		_, err = tx.Exec(ctx, "UPDATE build_targets SET input_hash=$4,requested_commit=$5 WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", ws, env, app, hash, commit)
	}
	if err != nil {
		return api.Operation{}, err
	}
	out, err := s.insertPipeline(ctx, tx, ws, env, key, server, pipeline.Command{Action: "build", Connection: connection, Commit: commit, Branch: c.Branch, RevisionID: base, UnitID: v.Unit, AppID: app, Build: &c, InputHash: hash, Registry: registry, SubmittedAt: time.Now().UTC()})
	return out, err
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
				data["result"] = json.RawMessage(result)
			}
			if len(artifact) > 0 {
				data["artifact"] = json.RawMessage(artifact)
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

func (s *Service) SourcePolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, enabled bool) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: enabled}, "git.source.policy", env, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		n, err := tx.Exec(ctx, "UPDATE git_sources SET configuration=jsonb_set(configuration,'{autoDeploy}',to_jsonb($3::boolean)),policy_actor=$4,status=CASE WHEN $3 AND status='approval_required' THEN 'ready' ELSE status END WHERE workspace_id=$1 AND environment_id=$2", ws, env, enabled, p.Account.ID)
		if err == nil && n.RowsAffected() != 1 {
			return domain.NotFound
		}
		return err
	})
}

func pipelineLifetime(p pipeline.Command) time.Duration {
	if p.IsBuild() && p.Build != nil {
		return time.Duration(p.Build.Limits().Minutes) * time.Minute
	}
	return 30 * time.Minute
}
