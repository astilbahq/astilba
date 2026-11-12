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
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// sealing begins a session that stores the given new values for an owner's integration
// settings, before the transaction that writes them and once p is admitted to it. done records
// whether they were stored.
func (s *Service) sealing(ctx context.Context, p store.Principal, ws uuid.UUID, items ...secretbroker.Item) (keys *secretbroker.Session, done func(stored bool), err error) {
	keys, err = s.Store.BeginKeys(ctx, p, ownerSettings.admission(), false, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Items: items})
	if err != nil {
		return nil, nil, err
	}
	return keys, func(stored bool) { keys.Close(ctx, secretbroker.UsedIf(stored)) }, nil
}

func sealIntegration(keys *secretbroker.Session, item secretbroker.Item, value any) ([]byte, error) {
	raw, err := stablejson.Marshal(value)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return keys.Seal(item, raw)
}

func openIntegration(keys *secretbroker.Session, item secretbroker.Item, raw []byte, value any) error {
	plain, err := keys.Open(item, raw)
	if err != nil {
		return err
	}
	defer clear(plain)
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
	item := secretbroker.Item{Kind: secretbroker.Git, ID: c.ID}
	keys := s.passKeys(secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Principal: p.Secrets(), Items: []secretbroker.Item{item}})
	defer keys.close(ctx)
	var repository *gitAnswer[repositoryIdentity]
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
			}}
		}
		if repository.Err != nil {
			return domain.Invalid("repository")
		}
		c.RepositoryID, c.Private = repository.Value.ID, repository.Value.Private
		if c.Private && !validCredential(access.Clone, true) {
			return domain.Invalid("clone")
		}
		access.Webhook, err = randtoken.New()
		if err != nil {
			return err
		}
		session, err := keys.get()
		if err != nil {
			return err
		}
		encrypted, err := sealIntegration(session, item, access)
		if err != nil {
			return err
		}
		keys.use()
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
	item := secretbroker.Item{Kind: secretbroker.Registry, ID: r.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return r, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "registry.create", r.ID, func(tx pgx.Tx) error {
		q := db.New(tx)
		count, err := q.CountRegistries(ctx, ws)
		if err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		encrypted, err := sealIntegration(keys, item, access)
		if err != nil {
			return err
		}
		sealed = true
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
	item := secretbroker.Item{Kind: secretbroker.BuildSecret, ID: v.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return v, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "build.secret.create", v.ID, func(tx pgx.Tx) error {
		q := db.New(tx)
		count, err := q.CountBuildSecrets(ctx, ws)
		if err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		cipher, err := keys.Seal(item, []byte(value))
		if err != nil {
			return err
		}
		sealed = true
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
	if json.Unmarshal(row.Configuration, &c) != nil {
		return domain.Unauthenticated
	}
	// Unauthenticated until the signature is checked, which needs the connection's secret: each
	// connection has a small budget of its own.
	item := secretbroker.Item{Kind: secretbroker.Git, ID: id}
	keys, err := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Operate, Principal: secretbroker.System("webhook"), Items: []secretbroker.Item{item}, Connection: &id})
	if err != nil {
		return err
	}
	err = openIntegration(keys, item, row.EncryptedAccess, &access)
	keys.Close(ctx, secretbroker.Done(err))
	if err != nil {
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
