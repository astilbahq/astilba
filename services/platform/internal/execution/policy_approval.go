package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// policyApproval says why a policy does or doesn't run now. role and current are the approver's
// role in the workspace and authentication version, nil when they left; approved is the version
// they approved with, nil once withdrawn; owner is whether the policy needs an owner rather than
// any member who can write; paused is a pause the policy itself records, such as a Git source
// waiting for approval after a restore. Only owners learn that an approver's credentials changed.
func policyApproval(role *string, current, approved *int64, owner, paused bool, viewer string) api.PolicyApproval {
	switch {
	case role == nil:
		return api.ApproverRemoved
	case *role != "owner" && (owner || !domain.CanWrite(*role)):
		return api.ApproverRole
	case approved == nil || paused:
		return api.ApprovalRequired
	case current == nil || *current != *approved:
		if viewer != "owner" {
			return api.ApprovalRequired
		}
		return api.ApproverCredentials
	}
	return api.Current
}

// sourceHash identifies a Git source's stored configuration and its approver, so an approval covers
// exactly what the approver was shown.
func sourceHash(configuration []byte, actor uuid.UUID) string {
	return pipeline.Hash(struct {
		Configuration jsontext.Value
		Actor         uuid.UUID
	}{configuration, actor})
}

// SourcePolicy turns a Git source's automatic deployment on or off. Turning it on approves the
// source's automatic imports and deployments as the caller's, which resumes a paused source.
// Turning it off approves nothing, so it needs no recent authentication and leaves the approval as
// it was.
func (s *Service) SourcePolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, enabled bool) error {
	action := "git.source.policy"
	if enabled {
		action = "git.source.approve"
	}
	// Turning automatic deployment off approves nothing, so a lapsed workspace can still do it.
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: enabled, Revocation: !enabled}, action, env, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		q := db.New(tx)
		var n int64
		var err error
		if enabled {
			n, err = q.ApproveGitSource(ctx, db.ApproveGitSourceParams{WorkspaceID: ws, EnvironmentID: env, AutoDeploy: true, PolicyActor: p.Account.ID, PolicyAuthVersion: p.Account.AuthVersion})
		} else {
			n, err = q.DisableGitSourceAutoDeploy(ctx, db.DisableGitSourceAutoDeployParams{WorkspaceID: ws, EnvironmentID: env})
		}
		if err == nil && n != 1 {
			return domain.NotFound
		}
		return err
	})
}

// ApproveSource approves a Git source's automatic imports and deployments as the caller's, keeping
// its configuration, which resumes a paused source. The hash is the one returned with the source,
// so a change made since it was shown is refused rather than approved unseen.
func (s *Service) ApproveSource(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, hash string) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.source.approve", env, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		q := db.New(tx)
		source, err := q.LockGitSourcePolicy(ctx, db.LockGitSourcePolicyParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		if sourceHash(source.Configuration, source.PolicyActor) != hash {
			return domain.Conflict
		}
		var cfg pipeline.Source
		if err = json.Unmarshal(source.Configuration, &cfg); err != nil {
			return err
		}
		_, err = q.ApproveGitSource(ctx, db.ApproveGitSourceParams{WorkspaceID: ws, EnvironmentID: env, AutoDeploy: cfg.AutoDeploy, PolicyActor: p.Account.ID, PolicyAuthVersion: p.Account.AuthVersion})
		return err
	})
}

// ApproveBuildTarget approves a build target's automatic builds and deployments as the caller's,
// which resumes a paused target. The hash is the one listed for it, so the approval covers exactly
// the configuration the caller saw; deploying needs an owner.
func (s *Service) ApproveBuildTarget(ctx context.Context, p store.Principal, ws, pid, env, app uuid.UUID, hash string) error {
	return s.scopeIn(ctx, p, ws, workspaceAccess{Write: true, Recent: true}, "build.approve", app, func(tx pgx.Tx, w store.Workspace) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		q := db.New(tx)
		target, err := q.LockBuildTargetPolicy(ctx, db.LockBuildTargetPolicyParams{WorkspaceID: ws, EnvironmentID: env, ApplicationID: app})
		if err != nil {
			return err
		}
		var cfg pipeline.BuildConfig
		if err = json.Unmarshal(target.Configuration, &cfg); err != nil {
			return err
		}
		if targetHash(cfg, target.PolicyActor) != hash {
			return domain.Conflict
		}
		if cfg.AutoDeploy && w.Role != "owner" {
			return domain.Forbidden
		}
		return q.ApproveBuildTargetPolicy(ctx, db.ApproveBuildTargetPolicyParams{WorkspaceID: ws, EnvironmentID: env, ApplicationID: app, PolicyActor: p.Account.ID, PolicyAuthVersion: p.Account.AuthVersion})
	})
}
