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
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Secret values are hydrated only at the authenticated agent transport boundary. With nil keys
// it makes the delivery's checks and opens nothing.
func (s *Service) hydrateVisual(ctx context.Context, keys valueKeys, c *api.ExecutionCommand) error {
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
	if keys == nil {
		return nil
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
		if err := openJSON(keys, secretbroker.Item{Kind: secretbroker.Registry, ID: *a.RegistryID}, registry.EncryptedAccess, &access); err != nil {
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
		plain, err := keys.open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: id}, v.EncryptedValue)
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

// validateVisualSecrets checks a plan's secrets fit a deployment payload from their stored
// lengths, opening only a version whose lengths are not stored yet.
func (s *Service) validateVisualSecrets(ctx context.Context, tx pgx.Tx, keys valueKeys, ws, env uuid.UUID, spec domain.Specification) error {
	if len(domain.SecretReferences(spec)) > 100 {
		return domain.Invalid("secrets")
	}
	total, encodedTotal := 0, 0
	for _, id := range domain.SecretReferences(spec) {
		secret, err := db.New(tx).GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return store.IfMissing(err, domain.Invalid("secretVersionId"))
		}
		if secret.PlaintextBytes.Valid && secret.JsonBytes.Valid {
			total += int(secret.PlaintextBytes.Int32)
			encodedTotal += int(secret.JsonBytes.Int32)
			continue
		}
		// A version the measuring job has not reached yet is opened to measure it.
		plain, err := keys.open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: id}, secret.EncryptedValue)
		if err != nil {
			return err
		}
		n, encoded := domain.SecretLengths(plain)
		clear(plain)
		total += n
		encodedTotal += encoded
	}
	if total > 65536 || encodedTotal > 140000 {
		return domain.Invalid("secrets")
	}
	return nil
}
