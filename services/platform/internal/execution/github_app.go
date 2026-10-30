package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type GitHubAuthorization struct {
	ID          uuid.UUID      `json:"id"`
	WorkspaceID uuid.UUID      `json:"workspaceId"`
	User        githubapp.User `json:"user"`
	ExpiresAt   time.Time      `json:"expiresAt"`
}
type githubAuthorizationSecret struct {
	Verifier string `json:"verifier,omitempty"`
	Token    string `json:"token,omitempty"`
}
type ConnectionSummary struct {
	pipeline.Connection
	Authentication string `json:"authentication"`
	Status         string `json:"status"`
}

func appConfiguration(raw []byte) bool {
	var v struct {
		Authentication string `json:"authentication"`
	}
	return json.Unmarshal(raw, &v) == nil && v.Authentication == "github_app"
}
func (s *Service) GitHubStatus(ctx context.Context, p store.Principal, ws uuid.UUID) (map[string]any, error) {
	out := map[string]any{"configured": s.GitHub != nil}
	err := s.scope(ctx, p, ws, workspaceAccess{Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		if s.GitHub != nil {
			out["name"] = s.GitHub.Config.Slug
			out["installUrl"] = s.GitHub.InstallURL()
		}
		return nil
	})
	return out, err
}
func (s *Service) StartGitHub(ctx context.Context, p store.Principal, ws uuid.UUID, session string) (map[string]any, error) {
	if session == "" {
		return nil, domain.Unauthenticated
	}
	if s.GitHub == nil {
		return nil, domain.Invalid("githubApp.notConfigured")
	}
	state, err := cryptobox.Token()
	if err != nil {
		return nil, err
	}
	verifier, err := cryptobox.Token()
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.github.authorize", id, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "DELETE FROM github_app_authorizations WHERE workspace_id=$1 AND expires_at<now()", ws); e != nil {
			return e
		}
		var total, active int
		if e := tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE account_id=$2 AND status IN ('pending','ready')) FROM github_app_authorizations WHERE workspace_id=$1", ws, p.Account.ID).Scan(&total, &active); e != nil {
			return e
		}
		if total >= 100 || active >= 5 {
			return domain.Conflict
		}
		secret, e := s.sealIntegration(ws, id, "github-authorization", githubAuthorizationSecret{Verifier: verifier})
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO github_app_authorizations(id,workspace_id,account_id,auth_version,session_hash,state_hash,app_id,encrypted_access,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',now()+interval '10 minutes')`, id, ws, p.Account.ID, p.Account.AuthVersion, hash(session), hash(state), s.GitHub.Config.ID, secret)
		return e
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"authorizationUrl": s.GitHub.AuthorizeURL(state, verifier)}, nil
}

// The row lock serializes code exchange and selection. The session nonce survives
// step-up, but never a new login. Neither public IDs nor an archive can grant access.
func (s *Service) gitHubGrant(ctx context.Context, tx pgx.Tx, p store.Principal, ws, id uuid.UUID, session string) (GitHubAuthorization, githubAuthorizationSecret, error) {
	v := GitHubAuthorization{ID: id, WorkspaceID: ws}
	var cipher []byte
	if s.GitHub == nil {
		return v, githubAuthorizationSecret{}, domain.Invalid("githubApp.notConfigured")
	}
	err := tx.QueryRow(ctx, `SELECT encrypted_access,COALESCE(github_user_id,0),github_login,expires_at FROM github_app_authorizations WHERE workspace_id=$1 AND id=$2 AND account_id=$3 AND auth_version=$4 AND session_hash=$5 AND app_id=$6 AND expires_at>now() AND status='ready' FOR UPDATE`, ws, id, p.Account.ID, p.Account.AuthVersion, hash(session), s.GitHub.Config.ID).Scan(&cipher, &v.User.ID, &v.User.Login, &v.ExpiresAt)
	var secret githubAuthorizationSecret
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Forbidden
	}
	if err == nil {
		err = s.openIntegration(ws, id, "github-authorization", cipher, &secret)
	}
	return v, secret, err
}
func (s *Service) CompleteGitHub(ctx context.Context, p store.Principal, session, state, code string) (GitHubAuthorization, error) {
	var out GitHubAuthorization
	if s.GitHub == nil || len(state) != 43 || code == "" || len(code) > 1024 {
		return out, domain.Invalid("githubApp.authorization")
	}
	var ws, id uuid.UUID
	if err := s.Store.Pool.QueryRow(ctx, "SELECT workspace_id,id FROM github_app_authorizations WHERE state_hash=$1", hash(state)).Scan(&ws, &id); err != nil {
		return out, domain.Forbidden
	}
	// Record a failed exchange even if GitHub consumed the code and the HTTP result
	// was lost; the browser must start again instead of replaying that external effect.
	var exchangeErr error
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		var cipher []byte
		var status string
		e := tx.QueryRow(ctx, `SELECT encrypted_access,status FROM github_app_authorizations WHERE workspace_id=$1 AND id=$2 AND account_id=$3 AND auth_version=$4 AND session_hash=$5 AND app_id=$6 AND expires_at>now() FOR UPDATE`, ws, id, p.Account.ID, p.Account.AuthVersion, hash(session), s.GitHub.Config.ID).Scan(&cipher, &status)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.Forbidden
		}
		if e != nil {
			return e
		}
		if status == "ready" {
			out, _, e = s.gitHubGrant(ctx, tx, p, ws, id, session)
			return e
		}
		if status != "pending" {
			return domain.Conflict
		}
		var secret githubAuthorizationSecret
		if e = s.openIntegration(ws, id, "github-authorization", cipher, &secret); e != nil {
			return e
		}
		token, user, e := s.GitHub.Exchange(ctx, code, secret.Verifier)
		if e != nil {
			exchangeErr = domain.Invalid("githubApp.authorization")
			secret = githubAuthorizationSecret{}
			status = "failed"
		} else {
			secret = githubAuthorizationSecret{Token: token}
			status = "ready"
		}
		cipher, e = s.sealIntegration(ws, id, "github-authorization", secret)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, "UPDATE github_app_authorizations SET encrypted_access=$3,status=$4,github_user_id=$5,github_login=$6 WHERE workspace_id=$1 AND id=$2 RETURNING id,workspace_id,expires_at", ws, id, cipher, status, user.ID, user.Login).Scan(&out.ID, &out.WorkspaceID, &out.ExpiresAt)
	})
	if err == nil && exchangeErr != nil {
		err = exchangeErr
	}
	if err == nil {
		err = s.Store.Pool.QueryRow(ctx, "SELECT github_user_id,github_login FROM github_app_authorizations WHERE id=$1", id).Scan(&out.User.ID, &out.User.Login)
	}
	return out, err
}
func (s *Service) GitHubInstallations(ctx context.Context, p store.Principal, ws, id uuid.UUID, session string, page int) ([]githubapp.Installation, error) {
	if page < 1 || page > 100 {
		return nil, domain.Invalid("page")
	}
	out := []githubapp.Installation{}
	err := s.scope(ctx, p, ws, workspaceAccess{Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		_, secret, e := s.gitHubGrant(ctx, tx, p, ws, id, session)
		if e != nil {
			return e
		}
		values, e := s.GitHub.Installations(ctx, secret.Token, page)
		if e != nil {
			return domain.Invalid("githubApp.installations")
		}
		out = values
		return nil
	})
	return out, err
}
func (s *Service) GitHubRepositories(ctx context.Context, p store.Principal, ws, id uuid.UUID, session string, installation int64, page int) ([]githubapp.Repository, error) {
	if installation < 1 || page < 1 || page > 100 {
		return nil, domain.Invalid("repository")
	}
	out := []githubapp.Repository{}
	err := s.scope(ctx, p, ws, workspaceAccess{Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		_, secret, e := s.gitHubGrant(ctx, tx, p, ws, id, session)
		if e != nil {
			return e
		}
		values, e := s.GitHub.Repositories(ctx, secret.Token, installation, page)
		if e != nil {
			return domain.Invalid("githubApp.repositories")
		}
		out = values
		return nil
	})
	return out, err
}

type GitHubConnect struct {
	Name, Repository             string
	RepositoryID, InstallationID int64
	ConnectionID                 *uuid.UUID
}

func (s *Service) ConnectGitHub(ctx context.Context, p store.Principal, ws, grant uuid.UUID, session string, in GitHubConnect) (ConnectionSummary, error) {
	var out ConnectionSummary
	name, e := domain.Name(in.Name)
	if e != nil {
		return out, e
	}
	if !pipeline.ValidRepository(in.Repository) || in.RepositoryID < 1 || in.InstallationID < 1 {
		return out, domain.Invalid("repository")
	}
	id := uuid.New()
	if in.ConnectionID != nil {
		id = *in.ConnectionID
	}
	var consumedToken string
	e = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.github.connect", id, func(tx pgx.Tx) error {
		auth, secret, e := s.gitHubGrant(ctx, tx, p, ws, grant, session)
		if e != nil {
			return e
		}
		// User-token endpoints intersect the user's repository access and this App's
		// access. Admin authority is required for the explicit workspace delegation.
		if _, e = s.GitHub.Repositories(ctx, secret.Token, in.InstallationID, 1); e != nil {
			return domain.Forbidden
		}
		repo, e := s.GitHub.UserRepository(ctx, secret.Token, in.Repository)
		if e != nil || repo.ID != in.RepositoryID || repo.FullName != in.Repository || !repo.Permissions.Admin {
			return domain.Forbidden
		}
		if e = s.GitHub.Authority(ctx, in.InstallationID, in.Repository); e != nil {
			return domain.Forbidden
		}
		c := pipeline.Connection{ID: id, Name: name, Provider: "github", BaseURL: "https://api.github.com", Repository: repo.FullName, RepositoryID: repo.ID, Private: repo.Private}
		if in.ConnectionID != nil {
			var raw []byte
			var old pipeline.Connection
			if e = tx.QueryRow(ctx, "SELECT configuration FROM git_connections WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, id).Scan(&raw); e != nil {
				return e
			}
			if json.Unmarshal(raw, &old) != nil || !appConfiguration(raw) || old.Repository != c.Repository || old.RepositoryID != c.RepositoryID || old.Private != c.Private {
				return domain.Conflict
			}
		} else {
			var count int
			if e = tx.QueryRow(ctx, "SELECT count(*) FROM git_connections WHERE workspace_id=$1", ws).Scan(&count); e != nil {
				return e
			}
			if count >= 100 {
				return domain.Conflict
			}
		}
		empty, e := s.sealIntegration(ws, id, "git", pipeline.Access{})
		if e != nil {
			return e
		}
		raw, e := json.Marshal(struct {
			pipeline.Connection
			Authentication string `json:"authentication"`
		}{c, "github_app"})
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET configuration=$3,encrypted_access=$4`, id, ws, raw, empty); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO github_app_connections(workspace_id,connection_id,app_id,installation_id,repository_id,github_user_id,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(connection_id) DO UPDATE SET app_id=$3,installation_id=$4,repository_id=$5,github_user_id=$6,actor_id=$7,generation=github_app_connections.generation+1,active=true`, ws, id, s.GitHub.Config.ID, in.InstallationID, repo.ID, auth.User.ID, p.Account.ID); e != nil {
			return e
		}
		erased, e := s.sealIntegration(ws, grant, "github-authorization", githubAuthorizationSecret{})
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE github_app_authorizations SET status='consumed',encrypted_access=$2 WHERE id=$1", grant, erased); e != nil {
			return e
		}
		consumedToken = secret.Token
		out = ConnectionSummary{c, "github_app", "connected"}
		return nil
	})
	if e == nil && consumedToken != "" {
		revokeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_ = s.GitHub.RevokeUser(revokeCtx, consumedToken)
	}
	return out, e
}
func (s *Service) DisconnectGitHub(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.github.disconnect", id, func(tx pgx.Tx) error {
		var raw []byte
		if e := tx.QueryRow(ctx, "SELECT configuration FROM git_connections WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw); e != nil {
			return e
		}
		if !appConfiguration(raw) {
			return domain.Invalid("githubApp.connection")
		}
		if e := cancelGitHubWork(ctx, tx, ws, id); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE github_app_connections SET active=false,generation=generation+1 WHERE workspace_id=$1 AND connection_id=$2", ws, id)
		return e
	})
}
func cancelGitHubWork(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) error {
	// Match Report: operation rows precede source rows and the App binding.
	_, e := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END WHERE workspace_id=$1 AND kind='pipeline' AND command->'pipeline'->'connection'->>'id'=$2 AND state NOT IN ('succeeded','failed','cancelled')`, ws, id.String())
	if e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE git_sources SET status='approval_required',configuration=jsonb_set(configuration,'{autoDeploy}','false') WHERE workspace_id=$1 AND connection_id=$2", ws, id); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE build_targets SET configuration=jsonb_set(jsonb_set(configuration,'{autoBuild}','false'),'{autoDeploy}','false') WHERE workspace_id=$1 AND connection_id=$2", ws, id); e != nil {
		return e
	}

	return nil
}
func (s *Service) gitAccess(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, write bool) (pipeline.Access, error) {
	var raw, cipher []byte
	if e := tx.QueryRow(ctx, "SELECT configuration,encrypted_access FROM git_connections WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw, &cipher); e != nil {
		return pipeline.Access{}, e
	}
	if !appConfiguration(raw) {
		var access pipeline.Access
		e := s.openIntegration(ws, id, "git", cipher, &access)
		return access, e
	}
	if s.GitHub == nil {
		return pipeline.Access{}, domain.Invalid("githubApp.reauthorizationRequired")
	}
	var app, installation, repo, generation int64
	if e := tx.QueryRow(ctx, "SELECT app_id,installation_id,repository_id,generation FROM github_app_connections WHERE workspace_id=$1 AND connection_id=$2 AND active FOR SHARE", ws, id).Scan(&app, &installation, &repo, &generation); e != nil {
		return pipeline.Access{}, domain.Invalid("githubApp.reauthorizationRequired")
	}
	var c pipeline.Connection
	if json.Unmarshal(raw, &c) != nil || c.Provider != "github" || c.BaseURL != "https://api.github.com" || c.RepositoryID != repo || app != s.GitHub.Config.ID {
		return pipeline.Access{}, domain.Forbidden
	}
	if e := s.GitHub.Validate(ctx, installation, repo, generation, c.Repository, c.Private); e != nil {
		return pipeline.Access{}, e
	}
	token, e := s.GitHub.Mint(ctx, installation, repo, write)
	if e != nil {
		return pipeline.Access{}, e
	}
	credential := pipeline.Credential{Username: "x-access-token", Password: token}
	return pipeline.Access{API: credential, Clone: credential}, nil
}
