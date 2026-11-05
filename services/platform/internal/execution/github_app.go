package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
		q := db.New(tx)
		if e := q.DeleteExpiredGitHubAuthorizations(ctx, ws); e != nil {
			return e
		}
		count, e := q.CountGitHubAuthorizations(ctx, db.CountGitHubAuthorizationsParams{WorkspaceID: ws, AccountID: p.Account.ID})
		if e != nil {
			return e
		}
		if count.Total >= 100 || count.Active >= 5 {
			return domain.Conflict
		}
		secret, e := s.sealIntegration(ws, id, "github-authorization", githubAuthorizationSecret{Verifier: verifier})
		if e != nil {
			return e
		}
		return q.InsertGitHubAuthorization(ctx, db.InsertGitHubAuthorizationParams{ID: id, WorkspaceID: ws, AccountID: p.Account.ID, AuthVersion: p.Account.AuthVersion, SessionHash: hash(session), StateHash: hash(state), AppID: s.GitHub.Config.ID, EncryptedAccess: secret})
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
	if s.GitHub == nil {
		return v, githubAuthorizationSecret{}, domain.Invalid("githubApp.notConfigured")
	}
	row, err := s.lockGitHubAuthorization(ctx, tx, p, ws, id, session)
	if err == nil && row.Status != "ready" {
		err = domain.Forbidden
	}
	var secret githubAuthorizationSecret
	if err == nil {
		v.User.ID, v.User.Login, v.ExpiresAt = row.GithubUserID, row.GithubLogin, row.ExpiresAt
		err = s.openIntegration(ws, id, "github-authorization", row.EncryptedAccess, &secret)
	}
	return v, secret, err
}

// lockGitHubAuthorization locks an authorization only for the account and login
// session that started it; any other caller receives Forbidden.
func (s *Service) lockGitHubAuthorization(ctx context.Context, tx pgx.Tx, p store.Principal, ws, id uuid.UUID, session string) (db.LockGitHubAuthorizationRow, error) {
	row, err := db.New(tx).LockGitHubAuthorization(ctx, db.LockGitHubAuthorizationParams{WorkspaceID: ws, ID: id, AccountID: p.Account.ID, AuthVersion: p.Account.AuthVersion, SessionHash: hash(session), AppID: s.GitHub.Config.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.Forbidden
	}
	return row, err
}
func (s *Service) CompleteGitHub(ctx context.Context, p store.Principal, session, state, code string) (GitHubAuthorization, error) {
	var out GitHubAuthorization
	if s.GitHub == nil || len(state) != 43 || code == "" || len(code) > 1024 {
		return out, domain.Invalid("githubApp.authorization")
	}
	authorization, err := s.Store.Queries.GitHubAuthorizationByState(ctx, hash(state))
	if err != nil {
		return out, store.IfMissing(err, domain.Forbidden)
	}
	ws, id := authorization.WorkspaceID, authorization.ID
	// Record a failed exchange even if GitHub consumed the code and the HTTP result
	// was lost; the browser must start again instead of replaying that external effect.
	var exchangeErr error
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		row, e := s.lockGitHubAuthorization(ctx, tx, p, ws, id, session)
		if e != nil {
			return e
		}
		cipher, status := row.EncryptedAccess, row.Status
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
		completed, e := db.New(tx).CompleteGitHubAuthorization(ctx, db.CompleteGitHubAuthorizationParams{WorkspaceID: ws, ID: id, EncryptedAccess: cipher, Status: status, GithubUserID: user.ID, GithubLogin: user.Login})
		out.ID, out.WorkspaceID, out.ExpiresAt = completed.ID, completed.WorkspaceID, completed.ExpiresAt
		return e
	})
	if err == nil && exchangeErr != nil {
		err = exchangeErr
	}
	if err == nil {
		var user db.GitHubAuthorizationUserRow
		user, err = s.Store.Queries.GitHubAuthorizationUser(ctx, db.GitHubAuthorizationUserParams{WorkspaceID: ws, ID: id})
		out.User.ID, out.User.Login = user.GithubUserID, user.GithubLogin
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
		q := db.New(tx)
		if in.ConnectionID != nil {
			var old pipeline.Connection
			raw, e := q.LockGitConnection(ctx, db.LockGitConnectionParams{WorkspaceID: ws, ID: id})
			if e != nil {
				return e
			}
			if json.Unmarshal(raw, &old) != nil || !appConfiguration(raw) || old.Repository != c.Repository || old.RepositoryID != c.RepositoryID || old.Private != c.Private {
				return domain.Conflict
			}
		} else {
			count, e := q.CountGitConnections(ctx, ws)
			if e != nil {
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
		raw, e := stablejson.Marshal(struct {
			pipeline.Connection
			Authentication string `json:"authentication"`
		}{c, "github_app"})
		if e != nil {
			return e
		}
		// The upserts update nothing for a connection ID owned by another workspace.
		n, e := q.UpsertGitHubAppGitConnection(ctx, db.UpsertGitHubAppGitConnectionParams{ID: id, WorkspaceID: ws, Configuration: raw, EncryptedAccess: empty})
		if e == nil && n == 0 {
			e = domain.NotFound
		}
		if e != nil {
			return e
		}
		n, e = q.UpsertGitHubAppConnection(ctx, db.UpsertGitHubAppConnectionParams{WorkspaceID: ws, ConnectionID: id, AppID: s.GitHub.Config.ID, InstallationID: in.InstallationID, RepositoryID: repo.ID, GithubUserID: auth.User.ID, ActorID: p.Account.ID})
		if e == nil && n == 0 {
			e = domain.NotFound
		}
		if e != nil {
			return e
		}
		erased, e := s.sealIntegration(ws, grant, "github-authorization", githubAuthorizationSecret{})
		if e != nil {
			return e
		}
		if e = q.ConsumeGitHubAuthorization(ctx, db.ConsumeGitHubAuthorizationParams{WorkspaceID: ws, ID: grant, EncryptedAccess: erased}); e != nil {
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
		q := db.New(tx)
		connection, e := q.GetGitConnection(ctx, db.GetGitConnectionParams{WorkspaceID: ws, ID: id})
		if e != nil {
			return e
		}
		if !appConfiguration(connection.Configuration) {
			return domain.Invalid("githubApp.connection")
		}
		if e := cancelGitHubWork(ctx, tx, ws, id); e != nil {
			return e
		}
		return q.DeactivateGitHubAppConnection(ctx, db.DeactivateGitHubAppConnectionParams{WorkspaceID: ws, ConnectionID: id})
	})
}
func cancelGitHubWork(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) error {
	q := db.New(tx)
	if e := q.CancelGitHubOperations(ctx, db.CancelGitHubOperationsParams{WorkspaceID: ws, ConnectionID: id.String()}); e != nil {
		return e
	}
	if e := q.PauseGitHubSources(ctx, db.PauseGitHubSourcesParams{WorkspaceID: ws, ConnectionID: id}); e != nil {
		return e
	}
	return q.PauseGitHubBuildTargets(ctx, db.PauseGitHubBuildTargetsParams{WorkspaceID: ws, ConnectionID: id})
}
func (s *Service) gitAccess(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, write bool) (pipeline.Access, error) {
	q := db.New(tx)
	connection, e := q.GetGitConnection(ctx, db.GetGitConnectionParams{WorkspaceID: ws, ID: id})
	if e != nil {
		return pipeline.Access{}, e
	}
	raw := connection.Configuration
	if !appConfiguration(raw) {
		var access pipeline.Access
		e := s.openIntegration(ws, id, "git", connection.EncryptedAccess, &access)
		return access, e
	}
	if s.GitHub == nil {
		return pipeline.Access{}, domain.Invalid("githubApp.reauthorizationRequired")
	}
	binding, e := q.ShareActiveGitHubAppConnection(ctx, db.ShareActiveGitHubAppConnectionParams{WorkspaceID: ws, ConnectionID: id})
	if e != nil {
		return pipeline.Access{}, store.IfMissing(e, domain.Invalid("githubApp.reauthorizationRequired"))
	}
	installation, repo := binding.InstallationID, binding.RepositoryID
	var c pipeline.Connection
	if json.Unmarshal(raw, &c) != nil || c.Provider != "github" || c.BaseURL != "https://api.github.com" || c.RepositoryID != repo || binding.AppID != s.GitHub.Config.ID {
		return pipeline.Access{}, domain.Forbidden
	}
	if e := s.GitHub.Validate(ctx, installation, repo, binding.Generation, c.Repository, c.Private); e != nil {
		return pipeline.Access{}, e
	}
	token, e := s.GitHub.Mint(ctx, installation, repo, write)
	if e != nil {
		return pipeline.Access{}, e
	}
	credential := pipeline.Credential{Username: "x-access-token", Password: token}
	return pipeline.Access{API: credential, Clone: credential}, nil
}
