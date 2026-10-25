package execution

import (
	"context"
	"encoding/json"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func targetHash(config pipeline.BuildConfig, actor uuid.UUID) string {
	return pipeline.Hash(struct {
		Configuration pipeline.BuildConfig
		Actor         uuid.UUID
	}{config.Canonical(), actor})
}

func (s *Service) ConfiguredBuildTargets(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.ConfiguredBuildTarget, error) {
	out := []api.ConfiguredBuildTarget{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT b.application_id,b.configuration,b.policy_actor FROM build_targets b
 JOIN memberships m ON m.workspace_id=b.workspace_id AND m.account_id=b.policy_actor
 WHERE b.workspace_id=$1 AND b.environment_id=$2 AND m.role='owner' ORDER BY b.application_id`, ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var app, actor uuid.UUID
			var raw []byte
			if err = rows.Scan(&app, &raw, &actor); err != nil {
				return err
			}
			var cfg pipeline.BuildConfig
			if err = json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
			for _, application := range v.Spec.Applications {
				if application.ID == app {
					out = append(out, api.ConfiguredBuildTarget{ApplicationId: app, BaseRevisionId: v.Revision, Name: application.Name, Branch: cfg.Branch, Builder: cfg.Canonical().Builder, TargetHash: targetHash(cfg, actor)})
				}
			}
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) RunConfiguredBuild(ctx context.Context, p store.Principal, ws, pid, env, app uuid.UUID, in api.RunConfiguredBuild) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, true, false, false, "build.run", app, func(tx pgx.Tx) error {
		old, exists, err := pipelineExisting(ctx, tx, ws, env, in.IdempotencyKey, "build")
		if err != nil {
			return err
		}
		if exists {
			var matches bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_runs r JOIN environments e ON e.workspace_id=r.workspace_id AND e.id=r.environment_id
 WHERE r.workspace_id=$1 AND e.project_id=$2 AND r.environment_id=$3 AND r.application_id=$4 AND r.id=$5)`, ws, pid, env, app, old.Id).Scan(&matches); err != nil {
				return err
			}
			if !matches {
				return domain.NotFound
			}
			out = old
			return nil
		}
		var raw []byte
		var actor uuid.UUID
		err = tx.QueryRow(ctx, `SELECT b.configuration,b.policy_actor FROM build_targets b
 JOIN memberships m ON m.workspace_id=b.workspace_id AND m.account_id=b.policy_actor
 WHERE b.workspace_id=$1 AND b.environment_id=$2 AND b.application_id=$3 AND m.role='owner'`, ws, env, app).Scan(&raw, &actor)
		if err != nil {
			return err
		}
		var cfg pipeline.BuildConfig
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if targetHash(cfg, actor) != in.TargetHash {
			return domain.Conflict
		}
		cfg.AutoBuild, cfg.AutoDeploy = false, false
		out, err = s.admitBuild(ctx, tx, p, ws, pid, env, in.BaseRevisionId, app, in.IdempotencyKey, cfg.Canonical(), false)
		return err
	})
	return out, err
}

func (s *Service) EnvironmentOperation(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var err error
		out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id))
		if err != nil {
			return err
		}
		if g := p.Automation; g != nil && !g.Allows(domain.EnvironmentRead) {
			allowed := out.Kind == "visual" && g.Allows(domain.DeploymentPlan, domain.DeploymentApply)
			if out.Kind == "pipeline" && g.Allows(domain.BuildRun) {
				if err = tx.QueryRow(ctx, "SELECT configuration->>'action'='build' FROM pipeline_runs WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&allowed); err != nil {
					return err
				}
			}
			if !allowed {
				return domain.Forbidden
			}
		}
		return nil
	})
	if err == nil {
		err = s.operationAvailability(ctx, &out)
	}
	return out, err
}
