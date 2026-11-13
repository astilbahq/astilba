package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Secret values are hydrated only at the authenticated agent transport boundary.
func (s *Service) hydrateVisual(ctx context.Context, c *api.ExecutionCommand) error {
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil && p.Action == "preview-cleanup" {
			return nil
		}
	}
	if c.Visual == nil {
		return errors.New("missing visual payload")
	}
	var payload deployment.Payload
	if json.Unmarshal(*c.Visual, &payload) != nil {
		return errors.New("invalid visual payload")
	}

	payload.Secrets = map[string]string{}
	var ws uuid.UUID
	if err := s.Store.Pool.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
		return err
	}
	if err := requireServiceOutputs(ctx, s.Store.Pool, ws, c.ServerId, payload.Plan.Specification); err != nil {
		return err
	}
	if err := requireReplicas(ctx, s.Store.Pool, ws, c.ServerId, payload.Plan); err != nil {
		return err
	}
	if len(payload.Plan.Instances) > 0 || payload.Plan.Release != nil {
		supported, err := releaseCapability(ctx, s.Store.Pool, ws, c.ServerId)
		if err != nil {
			return err
		}
		if !supported {
			return domain.Invalid("server.capabilities.safeReleases")
		}
	}
	payload.RegistryAccess = map[string]pipeline.Credential{}
	for key, a := range payload.Plan.Artifacts {
		if a.RegistryID == nil {
			continue
		}
		registry, err := s.Store.Queries.GetRegistry(ctx, db.GetRegistryParams{WorkspaceID: ws, ID: *a.RegistryID})
		if err != nil {
			return err
		}
		var access pipeline.RegistryAccess
		if err := s.legacyOpenIntegration(ws, *a.RegistryID, "registry", registry.EncryptedAccess, &access); err != nil {
			return err
		}
		payload.RegistryAccess[key] = access.Pull
	}
	total := 0
	refs := payload.Plan.SecretReferences()
	if payload.Action == "resolve" {
		refs = nil
	}
	for _, id := range refs {
		v, err := s.Store.Queries.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId, ID: id})
		if err != nil {
			return err
		}
		plain, err := s.Store.Keys.Open(v.EncryptedValue, store.SecretIdentity(ws, id))
		if err != nil {
			return err
		}
		total += len(plain)
		if total > 65536 {
			return errors.New("execution secret budget exceeded")
		}
		payload.Secrets[id.String()] = string(plain)
	}
	raw, err := stablejson.Marshal(payload)
	if err != nil || len(raw) > 250000 {
		return errors.New("execution payload limit exceeded")
	}
	c.Visual = rawMessage(raw)
	return nil
}

func (s *Service) validateVisualSecrets(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, spec domain.Specification) error {
	if len(domain.SecretReferences(spec)) > 100 {
		return domain.Invalid("secrets")
	}
	total, encodedTotal := 0, 0
	for _, id := range domain.SecretReferences(spec) {
		secret, err := db.New(tx).GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return store.IfMissing(err, domain.Invalid("secretVersionId"))
		}
		plain, err := s.Store.Keys.Open(secret.EncryptedValue, store.SecretIdentity(ws, id))
		if err != nil {
			return err
		}
		total += len(plain)
		escaped, _ := stablejson.Marshal(string(plain))
		encodedTotal += len(escaped)
	}
	if total > 65536 || encodedTotal > 140000 {
		return domain.Invalid("secrets")
	}
	return nil
}
