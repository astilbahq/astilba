package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
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
	state, err := randtoken.New()
	if err != nil {
		return nil, err
	}
	verifier, err := randtoken.New()
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

// The row lock serializes a grant's use, and a committed claim its code exchange. The session
// nonce survives step-up, but never a new login. Neither public IDs nor an archive can grant
// access.
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

// githubExchangeLimit covers a code exchange's two GitHub requests and recording the result.
const githubExchangeLimit = 45 * time.Second

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
	access := workspaceAccess{Write: true, Owner: true}
	// GitHub accepts a code once, so the attempt is recorded as failed, with its verifier
	// erased, before the code is sent. A lost reply, a crash or a repeated callback then
	// leaves a failed grant for the browser to start again, never a replayed exchange, and
	// no lock is held while GitHub answers.
	var verifier string
	var finished bool
	err = s.scope(ctx, p, ws, access, "", uuid.Nil, func(tx pgx.Tx) error {
		row, e := s.lockGitHubAuthorization(ctx, tx, p, ws, id, session)
		if e != nil {
			return e
		}
		if row.Status == "ready" {
			finished = true
			out, _, e = s.gitHubGrant(ctx, tx, p, ws, id, session)
			return e
		}
		if row.Status != "pending" {
			return domain.Conflict
		}
		var secret githubAuthorizationSecret
		if e = s.openIntegration(ws, id, "github-authorization", row.EncryptedAccess, &secret); e != nil {
			return e
		}
		verifier = secret.Verifier
		_, e = s.recordGitHubExchange(ctx, tx, ws, id, "failed", githubAuthorizationSecret{}, githubapp.User{})
		return e
	})
	if err == nil && !finished {
		// The code is spent from here, so a browser going away must not leave a token GitHub
		// issued unaccounted for: the exchange finishes, and the token is revoked below.
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubExchangeLimit)
		defer cancel()
		token, user, e := s.GitHub.Exchange(finish, code, verifier)
		if e != nil {
			if token != "" {
				s.revokeGitHubToken(finish, token)
			}
			return out, domain.Invalid("githubApp.authorization")
		}
		// The dashboard doesn't send a callback twice, so nobody could collect this grant.
		if ctx.Err() != nil {
			s.revokeGitHubToken(finish, token)
			return out, ctx.Err()
		}
		// Only this request can hold the claim, but its caller may have lost their role or
		// session during the call: the scope re-checks both, and decides if the token is kept.
		err = s.scope(finish, p, ws, access, "", uuid.Nil, func(tx pgx.Tx) error {
			if _, e := s.lockGitHubAuthorization(finish, tx, p, ws, id, session); e != nil {
				return e
			}
			out, e = s.recordGitHubExchange(finish, tx, ws, id, "ready", githubAuthorizationSecret{Token: token}, user)
			return e
		})
		if err != nil {
			s.revokeGitHubToken(finish, token)
		}
		out.User = user
	}
	return out, err
}
func (s *Service) recordGitHubExchange(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, status string, secret githubAuthorizationSecret, user githubapp.User) (GitHubAuthorization, error) {
	cipher, e := s.sealIntegration(ws, id, "github-authorization", secret)
	if e != nil {
		return GitHubAuthorization{}, e
	}
	completed, e := db.New(tx).CompleteGitHubAuthorization(ctx, db.CompleteGitHubAuthorizationParams{WorkspaceID: ws, ID: id, EncryptedAccess: cipher, Status: status, GithubUserID: user.ID, GithubLogin: user.Login})
	return GitHubAuthorization{ID: completed.ID, WorkspaceID: completed.WorkspaceID, ExpiresAt: completed.ExpiresAt}, e
}

// revokeGitHubToken is best effort, and outlives a caller who has gone away, because a user
// token that nothing stores would otherwise stay valid at GitHub.
func (s *Service) revokeGitHubToken(ctx context.Context, token string) {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = s.GitHub.RevokeUser(revokeCtx, token)
}

// githubGrantToken releases the grant's row before the caller lists anything with its
// token. A listing writes nothing, so nothing has to be checked again after it.
func (s *Service) githubGrantToken(ctx context.Context, p store.Principal, ws, id uuid.UUID, session string) (string, error) {
	var token string
	err := s.scope(ctx, p, ws, workspaceAccess{Owner: true}, "", uuid.Nil, func(tx pgx.Tx) error {
		_, secret, e := s.gitHubGrant(ctx, tx, p, ws, id, session)
		token = secret.Token
		return e
	})
	return token, err
}
func (s *Service) GitHubInstallations(ctx context.Context, p store.Principal, ws, id uuid.UUID, session string, page int) ([]githubapp.Installation, error) {
	if page < 1 || page > 100 {
		return nil, domain.Invalid("page")
	}
	token, err := s.githubGrantToken(ctx, p, ws, id, session)
	if err != nil {
		return []githubapp.Installation{}, err
	}
	values, e := s.GitHub.Installations(ctx, token, page)
	if e != nil {
		return []githubapp.Installation{}, domain.Invalid("githubApp.installations")
	}
	return values, nil
}
func (s *Service) GitHubRepositories(ctx context.Context, p store.Principal, ws, id uuid.UUID, session string, installation int64, page int) ([]githubapp.Repository, error) {
	if installation < 1 || page < 1 || page > 100 {
		return nil, domain.Invalid("repository")
	}
	token, err := s.githubGrantToken(ctx, p, ws, id, session)
	if err != nil {
		return []githubapp.Repository{}, err
	}
	values, e := s.GitHub.Repositories(ctx, token, installation, page)
	if e != nil {
		return []githubapp.Repository{}, domain.Invalid("githubApp.repositories")
	}
	return values, nil
}

type GitHubConnect struct {
	Name, Repository             string
	RepositoryID, InstallationID int64
	ConnectionID                 *uuid.UUID
}

// githubRepositoryAuthority answers for the user token it was asked with. User-token
// endpoints intersect the user's repository access and this App's access. Admin authority
// is required for the explicit workspace delegation.
func (s *Service) githubRepositoryAuthority(ctx context.Context, token string, in GitHubConnect) (githubapp.Repository, error) {
	if _, e := s.GitHub.Repositories(ctx, token, in.InstallationID, 1); e != nil {
		return githubapp.Repository{}, e
	}
	repo, e := s.GitHub.UserRepository(ctx, token, in.Repository)
	if e != nil {
		return repo, e
	}
	if repo.ID != in.RepositoryID || repo.FullName != in.Repository || !repo.Permissions.Admin {
		return repo, errors.New("repository identity or authority changed")
	}
	return repo, s.GitHub.Authority(ctx, in.InstallationID, in.Repository)
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
	var authority *gitAnswer[githubapp.Repository]
	// The pass that writes re-reads the grant under its lock, so a grant consumed or
	// invalidated during the calls stops here. A grant keeps one token until it is consumed.
	connect := func(tx pgx.Tx) error {
		auth, secret, e := s.gitHubGrant(ctx, tx, p, ws, grant, session)
		if e != nil {
			return e
		}
		if authority == nil {
			return &providerCall{fetch: func(ctx context.Context) {
				repo, e := s.githubRepositoryAuthority(ctx, secret.Token, in)
				authority = &gitAnswer[githubapp.Repository]{Value: repo, Err: e}
			}}
		}
		if authority.Err != nil {
			return domain.Forbidden
		}
		repo := authority.Value
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
	}
	e = withProviderCalls(ctx, func() error {
		return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.github.connect", id, connect)
	})
	if e == nil && consumedToken != "" {
		s.revokeGitHubToken(ctx, consumedToken)
	}
	return out, e
}
func (s *Service) DisconnectGitHub(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	// Disconnecting only takes access away, so a lapsed workspace can still do it.
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Revocation: true}, "git.github.disconnect", id, func(tx pgx.Tx) error {
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
