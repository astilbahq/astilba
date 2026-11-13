package execution

import (
	"context"
	"encoding/json/v2"

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

// ConfiguredBuildTargets lists an environment's build targets. An automation credential sees only
// those it can run: a target an owner approved, who is still an owner with the same credentials.
// A person sees every target of a current service, with whether it builds or deploys
// automatically, on what approval, and whether automation can run it.
func (s *Service) ConfiguredBuildTargets(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.ConfiguredBuildTarget, error) {
	out := []api.ConfiguredBuildTarget{}
	err := s.scopeIn(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx, w store.Workspace) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		// policy_approver_holds decides what runs; the approver's role and version only explain why
		// automatic work doesn't, in the terms of policyApproval.
		rows, err := tx.Query(ctx, `SELECT b.application_id,b.configuration,b.policy_actor,b.policy_auth_version,
 b.policy_owner AND policy_approver_holds(b.workspace_id,b.policy_actor,b.policy_auth_version,true),m.role,a.auth_version
 FROM build_targets b LEFT JOIN memberships m ON m.workspace_id=b.workspace_id AND m.account_id=b.policy_actor LEFT JOIN accounts a ON a.id=b.policy_actor
 WHERE b.workspace_id=$1 AND b.environment_id=$2 ORDER BY b.application_id`, ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var app, actor uuid.UUID
			var raw []byte
			var runnable bool
			var role *string
			var approved, current *int64
			if err = rows.Scan(&app, &raw, &actor, &approved, &runnable, &role, &current); err != nil {
				return err
			}
			if p.Automation != nil && !runnable {
				continue
			}
			var cfg pipeline.BuildConfig
			if err = json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
			for _, application := range v.Spec.Applications {
				if application.ID == app {
					target := api.ConfiguredBuildTarget{ApplicationId: app, BaseRevisionId: v.Revision, Name: application.Name, Branch: cfg.Branch, Builder: cfg.Canonical().Builder, TargetHash: targetHash(cfg, actor)}
					if p.Automation == nil {
						approval := policyApproval(role, current, approved, cfg.AutoDeploy, false, w.Role)
						target.AutomaticBuild, target.AutomaticDeploy, target.Approval, target.Runnable = &cfg.AutoBuild, &cfg.AutoDeploy, &approval, &runnable
					}
					out = append(out, target)
				}
			}
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) RunConfiguredBuild(ctx context.Context, p store.Principal, ws, pid, env, app uuid.UUID, in api.RunConfiguredBuild) (api.Operation, error) {
	var out api.Operation
	err := s.gitScope(ctx, p, ws, workspaceAccess{Write: true}, "build.run", app, func(tx pgx.Tx, git *gitAnswers) error {
		old, exists, err := existingBuild(ctx, tx, ws, pid, env, app, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if exists {
			out = old
			return nil
		}
		var raw []byte
		var actor uuid.UUID
		err = tx.QueryRow(ctx, `SELECT b.configuration,b.policy_actor FROM build_targets b
 WHERE b.workspace_id=$1 AND b.environment_id=$2 AND b.application_id=$3
  AND b.policy_owner AND policy_approver_holds(b.workspace_id,b.policy_actor,b.policy_auth_version,true)`, ws, env, app).Scan(&raw, &actor)
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
		out, err = s.admitBuild(ctx, tx, git, p, ws, pid, env, in.BaseRevisionId, app, in.IdempotencyKey, cfg.Canonical(), false)
		return err
	})
	return operationFor(p, out), err
}

func (s *Service) EnvironmentOperation(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		var err error
		out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id))
		if err != nil {
			return err
		}
		if !p.MayReadEnvironment() {
			allowed := out.Kind == "visual" && p.Automation.Allows(domain.DeploymentPlan, domain.DeploymentApply)
			if out.Kind == "pipeline" && p.Automation.Allows(domain.BuildRun) {
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
	return operationFor(p, out), err
}
