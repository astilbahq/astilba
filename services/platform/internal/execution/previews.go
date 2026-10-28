package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type previewConfig struct {
	Revision  uuid.UUID            `json:"baseRevisionId"`
	App       uuid.UUID            `json:"applicationId"`
	Build     pipeline.BuildConfig `json:"build"`
	MaxActive int                  `json:"maxActive"`
	MaxBuilds int                  `json:"maxConcurrentBuilds"`
	Limits    domain.PreviewLimits `json:"limits"`
	Grace     *int                 `json:"closeGraceHours"`
	Domains   bool                 `json:"generatedDomains"`
}

func (c previewConfig) validate() error {
	if c.Revision == uuid.Nil || c.App == uuid.Nil {
		return domain.Invalid("preview.template")
	}
	if c.MaxActive < 1 || c.MaxActive > 10 || c.MaxBuilds < 1 || c.MaxBuilds > 3 {
		return &domain.Error{Code: "invalid_input", Message: "Use 1–10 active previews and 1–3 concurrent builds.", Fields: []string{"maxActive", "maxConcurrentBuilds"}}
	}
	if c.MaxBuilds > c.MaxActive {
		return &domain.Error{Code: "invalid_input", Message: "Concurrent builds cannot exceed the maximum active previews.", Fields: []string{"maxConcurrentBuilds"}}
	}
	if c.Grace != nil && (*c.Grace < 0 || *c.Grace > 168) {
		return domain.Invalid("closeGraceHours")
	}

	if err := c.Limits.Validate(); err != nil {
		return err
	}
	if err := c.Build.Validate(); err != nil {
		return domain.Invalid("preview.build")
	}
	if c.Build.AutoBuild || c.Build.AutoDeploy || c.Build.RegistryID != nil || c.Build.FallbackRunnerID != nil || len(c.Build.Secrets) > 0 || c.Build.Budget == nil || len(c.Build.WatchPaths) > 0 {
		return domain.Invalid("preview.build")
	}
	return nil
}

type previewPolicy struct {
	Note                                                                       string
	ID, Workspace, Project, Template, Revision, App, Connection, Server, Actor uuid.UUID
	AuthVersion, Controller, Generation                                        int64
	Enabled                                                                    bool
	Config                                                                     previewConfig
}

const previewPolicyColumns = `id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,generation,enabled,configuration,note`

func scanPreviewPolicy(row pgx.Row) (previewPolicy, error) {
	var p previewPolicy
	var raw []byte
	err := row.Scan(&p.ID, &p.Workspace, &p.Project, &p.Template, &p.Revision, &p.App, &p.Connection, &p.Server, &p.Actor, &p.AuthVersion, &p.Controller, &p.Generation, &p.Enabled, &raw, &p.Note)
	if err == nil {
		err = json.Unmarshal(raw, &p.Config)
	}
	return p, err
}
func (p previewPolicy) response() api.PreviewPolicy {
	var cfg api.PreviewPolicyConfiguration
	raw, _ := json.Marshal(p.Config)
	_ = json.Unmarshal(raw, &cfg)
	return api.PreviewPolicy{Note: &p.Note, Id: p.ID, Generation: p.Generation, Enabled: p.Enabled, Configuration: cfg}
}
func (s *Service) PreviewPolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (api.PreviewPolicy, error) {
	var out api.PreviewPolicy
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		v, err := scanPreviewPolicy(tx.QueryRow(ctx, "SELECT "+previewPolicyColumns+" FROM preview_policies WHERE workspace_id=$1 AND project_id=$2 AND environment_id=$3", ws, pid, env))
		out = v.response()
		return err
	})
	return out, err
}
func (s *Service) SavePreviewPolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, in api.SavePreviewPolicy) (api.PreviewPolicy, error) {
	var out api.PreviewPolicy
	var cfg previewConfig
	raw, _ := json.Marshal(in.Configuration)
	if json.Unmarshal(raw, &cfg) != nil {
		return out, domain.Invalid("preview.policy")
	}
	cfg.Build = cfg.Build.Canonical()
	if err := cfg.validate(); err != nil {
		return out, err
	}
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: in.Enabled}, "preview.policy", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Purpose != "preview_template" {
			return domain.Conflict
		}
		old, err := scanPreviewPolicy(tx.QueryRow(ctx, "SELECT "+previewPolicyColumns+" FROM preview_policies WHERE workspace_id=$1 AND environment_id=$2", ws, env))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		exists := err == nil
		if !exists && in.Generation != 0 || exists && old.Generation != in.Generation {
			return domain.Conflict
		}
		var active bool
		if exists {
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM preview_requests WHERE policy_id=$1 AND environment_id IS NOT NULL AND state<>'deleted')", old.ID).Scan(&active); err != nil {
				return err
			}
		}
		if active && Hash(old.Config) != Hash(cfg) {
			return domain.Invalid("preview.activePolicyConfiguration")
		}
		if active && in.Enabled && (old.Actor != p.Account.ID || old.AuthVersion != p.Account.AuthVersion) {
			return domain.Invalid("preview.cleanupBeforeReapproval")
		}
		if !in.Enabled && exists && Hash(old.Config) == Hash(cfg) {
			_, err = tx.Exec(ctx, "UPDATE preview_policies SET enabled=false WHERE id=$1", old.ID)
			if err != nil {
				return err
			}
			if err = s.cancelPreviewWork(ctx, tx, ws, old.ID); err != nil {
				return err
			}
			old.Enabled = false
			out = old.response()
			return nil
		}

		if active && in.Enabled && Hash(old.Config) == Hash(cfg) {
			if err = s.previewOwnerAuthority(ctx, tx, old); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE preview_policies SET enabled=true,next_discovery_at=now() WHERE id=$1", old.ID); err != nil {
				return err
			}
			old.Enabled = true
			out = old.response()
			return s.enqueuePreview(ctx, tx, previewArgs{Policy: old.ID})
		}
		if v.Revision != cfg.Revision {
			return domain.Conflict
		}
		if err = domain.ValidatePreviewTemplate(v.Spec, cfg.App); err != nil {
			return err
		}
		if _, err = runnerEligible(ctx, tx, ws, cfg.Build.RunnerID, cfg.Build.Architecture, true, cfg.Build.UsesRailpack()); err != nil {
			return err
		}
		if _, err = s.visualServer(ctx, tx, ws, cfg.Build.RunnerID); err != nil {
			return err
		}
		var capability bool
		var controller int64
		if err = tx.QueryRow(ctx, "SELECT COALESCE((capabilities->>'previews')::boolean,false),controller_generation FROM servers WHERE workspace_id=$1 AND id=$2 AND role='combined' AND preview_only", ws, cfg.Build.RunnerID).Scan(&capability, &controller); err != nil {
			return err
		}
		if !capability {
			return domain.Invalid("server.capabilities.previews")
		}
		if cfg.Domains {
			var ready bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM server_domains WHERE workspace_id=$1 AND server_id=$2 AND base=$3 AND approved_controller=$4 AND state IN ('applied','manual'))", ws, cfg.Build.RunnerID, s.Store.ApplicationDomain, controller).Scan(&ready); err != nil {
				return err
			}
			if !ready || s.Store.ApplicationDomain == "" {
				return domain.Invalid("preview.generatedDomains")
			}
		}
		provider, _, err := s.previewProvider(ctx, tx, ws, cfg.Build.ConnectionID)
		if err != nil {
			return err
		}
		if _, err = provider.Resolve(ctx, cfg.Build.Branch); err != nil {
			return domain.Invalid("preview.branch")
		}
		if err = s.validateVisualSecrets(ctx, tx, ws, env, v.Spec); err != nil {
			return err
		}
		if _, err = s.buildInputs(ctx, tx, ws, env, cfg.Build); err != nil {
			return err
		}
		id, gen := uuid.New(), int64(1)
		if exists {
			id = old.ID
			gen = old.Generation + 1
		}
		raw, _ = json.Marshal(cfg)
		_, err = tx.Exec(ctx, `INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,generation,enabled,configuration)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
   ON CONFLICT(workspace_id,environment_id) DO UPDATE SET revision_id=EXCLUDED.revision_id,application_id=EXCLUDED.application_id,connection_id=EXCLUDED.connection_id,server_id=EXCLUDED.server_id,actor_id=EXCLUDED.actor_id,auth_version=EXCLUDED.auth_version,controller_generation=EXCLUDED.controller_generation,generation=EXCLUDED.generation,enabled=EXCLUDED.enabled,configuration=EXCLUDED.configuration,discovery_page=1,next_discovery_at=now()`, id, ws, pid, env, cfg.Revision, cfg.App, cfg.Build.ConnectionID, cfg.Build.RunnerID, p.Account.ID, p.Account.AuthVersion, controller, gen, in.Enabled, raw)
		if err != nil {
			return err
		}
		out = previewPolicy{ID: id, Generation: gen, Enabled: in.Enabled, Config: cfg}.response()
		return s.enqueuePreview(ctx, tx, previewArgs{Policy: id})
	})
	return out, err
}
func (s *Service) previewProvider(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (*pipeline.Provider, pipeline.Connection, error) {
	c, _, err := loadConnection(ctx, tx, ws, id)
	if err != nil {
		return nil, c, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT configuration FROM git_connections WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw); err != nil {
		return nil, c, err
	}
	access, err := s.gitAccess(ctx, tx, ws, id, false)
	if err != nil {
		return nil, c, err
	}
	if appConfiguration(raw) {
		var installation, repo int64
		if err = tx.QueryRow(ctx, "SELECT installation_id,repository_id FROM github_app_connections WHERE workspace_id=$1 AND connection_id=$2 AND active", ws, id).Scan(&installation, &repo); err != nil {
			return nil, c, err
		}
		token, e := s.GitHub.MintPreview(ctx, installation, repo)
		if e != nil {
			return nil, c, e
		}
		access.API = pipeline.Credential{Username: "x-access-token", Password: token}
	}
	provider, err := pipeline.NewProvider(c, access.API, s.Policy)
	return provider, c, err
}
func (s *Service) previewOwnerAuthority(ctx context.Context, tx pgx.Tx, p previewPolicy) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a JOIN memberships m ON m.account_id=a.id JOIN servers s ON s.workspace_id=m.workspace_id WHERE a.id=$1 AND a.auth_version=$2 AND m.workspace_id=$3 AND m.role='owner' AND s.id=$4 AND s.controller_generation=$5 AND s.preview_only AND s.role='combined' AND NOT s.revoked AND NOT s.awaiting_transfer AND COALESCE((s.capabilities->>'previews')::boolean,false))`, p.Actor, p.AuthVersion, p.Workspace, p.Server, p.Controller).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Forbidden
	}
	return nil
}
func (s *Service) previewAuthority(ctx context.Context, tx pgx.Tx, p previewPolicy) error {
	if !p.Enabled {
		return domain.Forbidden
	}
	if err := s.previewOwnerAuthority(ctx, tx, p); err != nil {
		return err
	}
	return s.Store.ManagementAdmission(ctx, tx, p.Workspace)
}
func (s *Service) previewCommandBoundary(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	var reserved bool
	if err := tx.QueryRow(ctx, "SELECT preview_only FROM servers WHERE workspace_id=$1 AND id=$2", ws, c.ServerId).Scan(&reserved); err != nil {
		return err
	}
	preview := false
	if c.Pipeline != nil {
		var p pipeline.Command
		if json.Unmarshal(*c.Pipeline, &p) == nil {
			preview = p.Preview != nil
		}
	}
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil {
			preview = p.Plan.Preview != nil
		}
	}
	if c.Kind != "update" && reserved != preview {
		return domain.Forbidden
	}
	if c.EnvironmentId == uuid.Nil {
		return nil
	}
	var purpose string
	if err := tx.QueryRow(ctx, "SELECT purpose FROM environments WHERE workspace_id=$1 AND id=$2", ws, c.EnvironmentId).Scan(&purpose); err != nil {
		return err
	}
	switch purpose {
	case "preview_template":
		var p pipeline.Command
		if c.Kind != "pipeline" || c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Action != "build" || p.Build == nil || p.Build.AutoDeploy {
			return domain.Forbidden
		}
	case "preview":
		if c.Kind == "pipeline" {
			var p pipeline.Command
			if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Preview == nil {
				return domain.Forbidden
			}
		} else if c.Kind == "visual" {
			var p deployment.Payload
			if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Plan.Preview == nil {
				return domain.Forbidden
			}
		} else {
			return domain.Forbidden
		}
	}
	return nil
}
func (s *Service) cancelPreviewWork(ctx context.Context, tx pgx.Tx, ws, policy uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE preview_requests r SET resume_after_cancel=r.resume_after_cancel OR EXISTS(SELECT 1 FROM execution_operations o WHERE o.workspace_id=r.workspace_id AND o.environment_id=r.environment_id AND o.state NOT IN ('succeeded','failed','cancelled')) WHERE r.workspace_id=$1 AND r.policy_id=$2 AND r.state<>'deleted'`, ws, policy); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE execution_operations o SET cancel_requested=true WHERE o.workspace_id=$2 AND o.state NOT IN ('succeeded','failed','cancelled') AND o.environment_id IN (SELECT environment_id FROM preview_requests WHERE policy_id=$1) AND NOT COALESCE(o.command->'visual'->>'action'='preview-cleanup',false)`, policy, ws)
	return err
}

const previewColumns = `id,number,head,head_repository,fork,state,note,generation,environment_id,operation_id,cleanup_at`

func scanPreview(row pgx.Row) (api.PullRequestPreview, error) {
	var v api.PullRequestPreview
	err := row.Scan(&v.Id, &v.Number, &v.Head, &v.HeadRepository, &v.Fork, &v.State, &v.Note, &v.Generation, &v.EnvironmentId, &v.OperationId, &v.CleanupAt)
	return v, err
}
func (s *Service) Previews(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.PullRequestPreview, error) {
	out := []api.PullRequestPreview{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+previewColumns+" FROM preview_requests WHERE workspace_id=$1 AND policy_id IN (SELECT id FROM preview_policies WHERE workspace_id=$1 AND project_id=$2 AND environment_id=$3) ORDER BY (state='deleted'),updated_at DESC,id LIMIT 100", ws, pid, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanPreview(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) ActOnPreview(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.PreviewAction) (api.PullRequestPreview, error) {
	var out api.PullRequestPreview
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: in.Action == "cleanup"}, "preview."+string(in.Action), id, func(tx pgx.Tx) error {
		policy, err := scanPreviewPolicy(tx.QueryRow(ctx, "SELECT "+previewPolicyColumns+" FROM preview_policies WHERE workspace_id=$1 AND project_id=$2 AND environment_id=$3", ws, pid, env))
		if err != nil {
			return err
		}
		out, err = scanPreview(tx.QueryRow(ctx, "SELECT "+previewColumns+" FROM preview_requests WHERE workspace_id=$1 AND policy_id=$2 AND id=$3", ws, policy.ID, id))
		if err != nil {
			return err
		}
		if out.Head != in.Head || out.HeadRepository != in.HeadRepository {
			return domain.Conflict
		}
		switch string(in.Action) {
		case "approve":
			if err = s.previewAuthority(ctx, tx, policy); err != nil {
				return err
			}
			provider, _, err := s.previewProvider(ctx, tx, ws, policy.Connection)
			if err != nil {
				return err
			}
			r, err := provider.PullRequest(ctx, out.Number)
			if err != nil {
				return err
			}
			if !r.Open || !r.Fork || r.Head != out.Head || r.HeadRepository != out.HeadRepository || r.BaseBranch != policy.Config.Build.Branch {
				return domain.Conflict
			}
			_, err = tx.Exec(ctx, "UPDATE preview_requests SET approved_head=$2,approved_repository=$3,approved_generation=$4,next_check_at=now() WHERE id=$1", id, r.Head, r.HeadRepository, policy.Generation)
			if err != nil {
				return err
			}
		case "cleanup":
			var controller int64
			if err = tx.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND NOT awaiting_transfer", ws, policy.Server).Scan(&controller); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE preview_requests SET cleanup_requested=true,cleanup_at=now(),next_check_at=now(),cleanup_actor=$2,cleanup_auth_version=$3,cleanup_controller=$4 WHERE id=$1", id, p.Account.ID, p.Account.AuthVersion, controller)
			if err != nil {
				return err
			}
		case "refresh":
			if out.State == "failed" || out.State == "blocked" {
				busy, e := previewBusy(ctx, tx, ws, out.EnvironmentId)
				if e != nil {
					return e
				}
				if busy {
					return domain.Conflict
				}
				if _, err = tx.Exec(ctx, "UPDATE preview_requests SET build_id=NULL,plan_id=NULL,operation_id=NULL,built_head='' WHERE id=$1", id); err != nil {
					return err
				}
			}

			if out.State == "deleted" {
				if _, err = tx.Exec(ctx, "UPDATE preview_requests SET cleanup_requested=false,cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL WHERE id=$1", id); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, "UPDATE preview_requests SET next_check_at=now() WHERE id=$1", id)
			if err != nil {
				return err
			}
		default:
			return domain.Invalid("preview.action")
		}
		if err = s.enqueuePreview(ctx, tx, previewArgs{Request: id}); err != nil {
			return err
		}
		return nil
	})
	return out, err
}
