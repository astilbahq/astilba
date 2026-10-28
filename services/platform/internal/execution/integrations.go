package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func integrationIdentity(ws, id uuid.UUID, kind string) string {
	return pipeline.EncryptionIdentity(ws, id, kind)
}
func (s *Service) sealIntegration(ws, id uuid.UUID, kind string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
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
		rows, err := tx.Query(ctx, "SELECT c.configuration,COALESCE(a.active,false),COALESCE(a.app_id,0) FROM git_connections c LEFT JOIN github_app_connections a ON a.workspace_id=c.workspace_id AND a.connection_id=c.id WHERE c.workspace_id=$1 ORDER BY c.id LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var c pipeline.Connection
			var active bool
			var app int64
			if err := rows.Scan(&raw, &active, &app); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			summary := ConnectionSummary{c, "manual", "connected"}
			if appConfiguration(raw) {
				summary.Authentication = "github_app"
				if !active || s.GitHub == nil || app != s.GitHub.Config.ID {
					summary.Status = "reauthorization_required"
				}
			}
			out = append(out, summary)
		}
		return rows.Err()
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
	// Validate authority before contacting a configured integration.
	err = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "git.connection.create", c.ID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM git_connections WHERE workspace_id=$1", ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		var err error
		c.RepositoryID, c.Private, err = provider.Repository(ctx)
		if err != nil {
			return domain.Invalid("repository")
		}
		if c.Private && !validCredential(access.Clone, true) {
			return domain.Invalid("clone")
		}
		access.Webhook, err = cryptobox.Token()
		if err != nil {
			return err
		}
		encrypted, err := s.sealIntegration(ws, c.ID, "git", access)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(c)
		_, err = tx.Exec(ctx, "INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)", c.ID, ws, raw, encrypted)
		return err
	})
	return c, access.Webhook, err
}
func loadConnection(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (pipeline.Connection, []byte, error) {
	var c pipeline.Connection
	var raw, secret []byte
	err := tx.QueryRow(ctx, "SELECT configuration,encrypted_access FROM git_connections WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw, &secret)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, secret, err
}
func (s *Service) provider(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (*pipeline.Provider, pipeline.Connection, error) {
	c, _, err := loadConnection(ctx, tx, ws, id)
	if err != nil {
		return nil, c, err
	}
	access, err := s.gitAccess(ctx, tx, ws, id, false)
	if err != nil {
		return nil, c, err
	}
	provider, err := pipeline.NewProvider(c, access.API, s.Policy)
	return provider, c, err
}
func (s *Service) Registries(ctx context.Context, p store.Principal, ws uuid.UUID) ([]pipeline.Registry, error) {
	out := []pipeline.Registry{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT configuration FROM registries WHERE workspace_id=$1 ORDER BY id LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var v pipeline.Registry
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
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
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM registries WHERE workspace_id=$1", ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		encrypted, err := s.sealIntegration(ws, r.ID, "registry", access)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(r)
		_, err = tx.Exec(ctx, "INSERT INTO registries(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)", r.ID, ws, raw, encrypted)
		return err
	})
	return r, err
}
func loadRegistry(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (pipeline.Registry, []byte, error) {
	var v pipeline.Registry
	var raw, access []byte
	err := tx.QueryRow(ctx, "SELECT configuration,encrypted_access FROM registries WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw, &access)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return v, access, err
}
func (s *Service) BuildSecrets(ctx context.Context, p store.Principal, ws uuid.UUID) ([]pipeline.BuildSecret, error) {
	out := []pipeline.BuildSecret{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id,name,created_at FROM build_secret_versions WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v pipeline.BuildSecret
			if err := rows.Scan(&v.ID, &v.Name, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
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
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM build_secret_versions WHERE workspace_id=$1", ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		cipher, err := s.Store.Keys.Seal([]byte(value), integrationIdentity(ws, v.ID, "build-secret"))
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "INSERT INTO build_secret_versions(id,workspace_id,name,encrypted_value) VALUES($1,$2,$3,$4) RETURNING created_at", v.ID, ws, v.Name, cipher).Scan(&v.CreatedAt)
	})
	return v, err
}
func (s *Service) ReceivePush(ctx context.Context, id uuid.UUID, headers http.Header, body []byte) error {
	var ws uuid.UUID
	var raw, encrypted []byte
	if err := s.Store.Pool.QueryRow(ctx, "SELECT workspace_id,configuration,encrypted_access FROM git_connections WHERE id=$1", id).Scan(&ws, &raw, &encrypted); err != nil {
		return domain.NotFound
	}
	if appConfiguration(raw) {
		return domain.Forbidden
	}
	var c pipeline.Connection
	var access pipeline.Access
	if json.Unmarshal(raw, &c) != nil || s.openIntegration(ws, id, "git", encrypted, &access) != nil {
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
	// A connection lock bounds both delivery retention and coalesced refresh scheduling.
	if _, err = tx.Exec(ctx, "SELECT id FROM git_connections WHERE id=$1 FOR UPDATE", id); err != nil {
		return err
	}
	var previous string
	err = tx.QueryRow(ctx, "SELECT body_hash FROM git_deliveries WHERE connection_id=$1 AND delivery_id=$2", id, delivery).Scan(&previous)
	if err == nil {
		if previous != pipeline.Hash(string(body)) {
			return domain.Conflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO git_deliveries(workspace_id,connection_id,delivery_id,body_hash) VALUES($1,$2,$3,$4)", ws, id, delivery, pipeline.Hash(string(body))); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM git_deliveries WHERE connection_id=$1 AND delivery_id NOT IN (SELECT delivery_id FROM git_deliveries WHERE connection_id=$1 ORDER BY received_at DESC LIMIT 1000)", id); err != nil {
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
