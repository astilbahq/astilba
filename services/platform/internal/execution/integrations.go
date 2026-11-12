package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func integrationIdentity(ws, id uuid.UUID, kind string) string {
	return pipeline.EncryptionIdentity(ws, id, kind)
}
func (s *Service) sealIntegration(ws, id uuid.UUID, kind string, value any) ([]byte, error) {
	raw, err := stablejson.Marshal(value)
	if err != nil {
		return nil, err
	}
	return s.Store.Keys.Seal(raw, integrationIdentity(ws, id, kind))
}
func (s *Service) openIntegration(ws, id uuid.UUID, kind string, raw []byte, value any) error {
	plain, err := s.Store.Keys.Open(raw, integrationIdentity(ws, id, kind))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, value)
}
func validCredential(c pipeline.Credential, required bool) bool {
	return (!required || c.Username != "" && c.Password != "") && len(c.Username) <= 200 && len(c.Password) <= 8192 && !strings.ContainsAny(c.Username+c.Password, "\x00\r\n")
}
func (s *Service) Connections(ctx context.Context, p store.Principal, ws uuid.UUID) ([]ConnectionSummary, error) {
	out := []ConnectionSummary{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListGitConnections(ctx, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var c pipeline.Connection
			if err := json.Unmarshal(row.Configuration, &c); err != nil {
				return err
			}
			summary := ConnectionSummary{c, "manual", "connected"}
			if appConfiguration(row.Configuration) {
				summary.Authentication = "github_app"
				if !row.AppActive || s.GitHub == nil || row.AppID != s.GitHub.Config.ID {
					summary.Status = "reauthorization_required"
				}
			}
			out = append(out, summary)
		}
		return nil
	})
	return out, err
}
func (s *Service) CreateConnection(ctx context.Context, p store.Principal, ws uuid.UUID, c pipeline.Connection, access pipeline.Access) (pipeline.Connection, string, error) {
	c.ID = uuid.New()
	if c.Provider == "github" && c.BaseURL == "https://github.com" {
		c.BaseURL = "https://api.github.com"
	}
	name, err := domain.Name(c.Name)
	if err != nil {
		return c, "", err
	}
	c.Name = name
	if !validCredential(access.API, false) || !validCredential(access.Clone, false) {
		return c, "", domain.Invalid("credential")
	}
	provider, err := pipeline.NewProvider(c, access.API, s.Policy)
	if err != nil {
		return c, "", domain.Invalid("provider")
	}
	// Validate authority before contacting a configured integration. The answer is about
	// this request's own URL and credential, so only the limit needs checking again.
	var repository *gitAnswer[repositoryIdentity]
	var push *gitAnswer[bool]
	create := func(tx pgx.Tx) error {
		q := db.New(tx)
		count, err := q.CountGitConnections(ctx, ws)
		if err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		if repository == nil {
			return &providerCall{fetch: func(ctx context.Context) {
				id, private, err := provider.Repository(ctx)
				repository = &gitAnswer[repositoryIdentity]{Value: repositoryIdentity{id, private}, Err: err}
				// Runners receive the clone credential, so one that could push is refused here
				// rather than at the first build.
				push = &gitAnswer[bool]{}
				if err == nil && access.Clone.Password != "" {
					clone, err := pipeline.NewProvider(c, access.Clone, s.Policy)
					if err == nil {
						push.Value, push.Err = clone.CanPush(ctx)
					} else {
						push.Err = err
					}
				}
			}}
		}
		if repository.Err != nil {
			return domain.Invalid("repository")
		}
		if errors.Is(push.Err, pipeline.ErrCredentialRejected) {
			return errCloneRejected
		}
		if push.Err != nil {
			return &domain.Error{Code: "invalid_input", Message: "The Git server did not say whether the clone credential can push, so it was not saved. Try again.", Fields: []string{"clone"}}
		}
		if push.Value {
			return errCloneCanPush
		}
		c.RepositoryID, c.Private = repository.Value.ID, repository.Value.Private
		if c.Private && !validCredential(access.Clone, true) {
			return domain.Invalid("clone")
		}
		access.Webhook, err = randtoken.New()
		if err != nil {
			return err
		}
		encrypted, err := s.sealIntegration(ws, c.ID, "git", access)
		if err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(c)
		return q.InsertGitConnection(ctx, db.InsertGitConnectionParams{ID: c.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: encrypted})
	}
	err = withProviderCalls(ctx, func() error {
		return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.connection.create", c.ID, create)
	})
	return c, access.Webhook, err
}
func (s *Service) Registries(ctx context.Context, p store.Principal, ws uuid.UUID) ([]pipeline.Registry, error) {
	out := []pipeline.Registry{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListRegistries(ctx, ws)
		if err != nil {
			return err
		}
		for _, raw := range rows {
			var v pipeline.Registry
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}
func (s *Service) CreateRegistry(ctx context.Context, p store.Principal, ws uuid.UUID, r pipeline.Registry, access pipeline.RegistryAccess) (pipeline.Registry, error) {
	r.ID = uuid.New()
	name, err := domain.Name(r.Name)
	if err != nil {
		return r, err
	}
	r.Name = name
	if !pipeline.ValidRegistry(r.Repository) || !validCredential(access.Push, true) || !validCredential(access.Pull, true) {
		return r, domain.Invalid("registry")
	}
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "registry.create", r.ID, func(tx pgx.Tx) error {
		q := db.New(tx)
		count, err := q.CountRegistries(ctx, ws)
		if err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		encrypted, err := s.sealIntegration(ws, r.ID, "registry", access)
		if err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(r)
		return q.InsertRegistry(ctx, db.InsertRegistryParams{ID: r.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: encrypted})
	})
	return r, err
}
func loadRegistry(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (pipeline.Registry, []byte, error) {
	var v pipeline.Registry
	row, err := db.New(tx).GetRegistry(ctx, db.GetRegistryParams{WorkspaceID: ws, ID: id})
	if err == nil {
		err = json.Unmarshal(row.Configuration, &v)
	}
	return v, row.EncryptedAccess, err
}
func (s *Service) BuildSecrets(ctx context.Context, p store.Principal, ws uuid.UUID) ([]pipeline.BuildSecret, error) {
	out := []pipeline.BuildSecret{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListBuildSecrets(ctx, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out = append(out, pipeline.BuildSecret{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt})
		}
		return nil
	})
	return out, err
}
func (s *Service) CreateBuildSecret(ctx context.Context, p store.Principal, ws uuid.UUID, name, value string) (pipeline.BuildSecret, error) {
	v := pipeline.BuildSecret{ID: uuid.New()}
	var err error
	v.Name, err = domain.Name(name)
	if err != nil {
		return v, err
	}
	if value == "" || len(value) > 4096 || strings.ContainsRune(value, 0) {
		return v, domain.Invalid("value")
	}
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "build.secret.create", v.ID, func(tx pgx.Tx) error {
		q := db.New(tx)
		count, err := q.CountBuildSecrets(ctx, ws)
		if err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		cipher, err := s.Store.Keys.Seal([]byte(value), integrationIdentity(ws, v.ID, "build-secret"))
		if err != nil {
			return err
		}
		v.CreatedAt, err = q.InsertBuildSecret(ctx, db.InsertBuildSecretParams{ID: v.ID, WorkspaceID: ws, Name: v.Name, EncryptedValue: cipher})
		return err
	})
	return v, err
}
func (s *Service) ReceivePush(ctx context.Context, id uuid.UUID, headers http.Header, body []byte) error {
	row, err := s.Store.Queries.GitConnectionForWebhook(ctx, id)
	if err != nil {
		return err
	}
	ws := row.WorkspaceID
	if appConfiguration(row.Configuration) {
		return domain.Forbidden
	}
	var c pipeline.Connection
	var access pipeline.Access
	if json.Unmarshal(row.Configuration, &c) != nil || s.openIntegration(ws, id, "git", row.EncryptedAccess, &access) != nil {
		return domain.Unauthenticated
	}
	push, pushErr := pipeline.VerifyPush(c.Provider, access.Webhook, c.RepositoryID, headers, body)
	preview, previewErr := pipeline.VerifyPullRequest(c.Provider, access.Webhook, c.RepositoryID, headers, body)
	if pushErr != nil && previewErr != nil {
		return domain.Forbidden
	}
	delivery := push.Delivery
	if previewErr == nil {
		delivery = preview.Delivery
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	// A connection lock bounds both delivery retention and coalesced refresh scheduling.
	if err = q.LockGitConnectionDeliveries(ctx, db.LockGitConnectionDeliveriesParams{WorkspaceID: ws, ID: id}); err != nil {
		return err
	}
	previous, err := q.GitDeliveryBodyHash(ctx, db.GitDeliveryBodyHashParams{WorkspaceID: ws, ConnectionID: id, DeliveryID: delivery})
	if err == nil {
		if previous != pipeline.Hash(string(body)) {
			return domain.Conflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err = q.InsertGitDelivery(ctx, db.InsertGitDeliveryParams{WorkspaceID: ws, ConnectionID: id, DeliveryID: delivery, BodyHash: pipeline.Hash(string(body))}); err != nil {
		return err
	}
	if err = q.PruneGitDeliveries(ctx, db.PruneGitDeliveriesParams{WorkspaceID: ws, ConnectionID: id}); err != nil {
		return err
	}
	if previewErr == nil {
		if err = s.enqueueConnectionPreviews(ctx, tx, ws, id, preview.Number); err != nil {
			return err
		}
	} else if err = s.enqueueGitRefresh(ctx, tx, ws, id, push); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
