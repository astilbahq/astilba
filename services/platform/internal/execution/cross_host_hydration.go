package execution

import (
	"context"
	"encoding/json"
	"errors"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Service) hydrateCrossHost(ctx context.Context, c *api.ExecutionCommand) error {
	var p deployment.CrossHostCommand
	if c.ParentId == nil || c.Crosshost == nil || hostnetwork.Decode(*c.Crosshost, &p) != nil || p.ParentID != *c.ParentId || p.Digest() != c.PlanHash || p.Unit.Plan.ServerID != c.ServerId || p.Unit.Plan.EnvironmentID != c.EnvironmentId {
		return domain.Forbidden
	}
	var ws uuid.UUID
	var enabled bool
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id,NOT revoked AND NOT awaiting_transfer AND NOT preview_only AND role<>'build' AND COALESCE((capabilities->>'crossHost')::boolean,false) FROM servers WHERE id=$1`, c.ServerId).Scan(&ws, &enabled); err != nil {
		return err
	}
	if !enabled {
		return domain.Forbidden
	}
	p.Secrets = map[string]string{}
	p.RegistryAccess = map[string]pipeline.Credential{}
	if p.Phase == "resolve" || p.Phase == "prepare" || p.Phase == "hook" {
		for key, a := range p.Unit.Plan.Artifacts {
			if a.RegistryID == nil {
				continue
			}
			var cipher []byte
			if err := s.Store.Pool.QueryRow(ctx, `SELECT encrypted_access FROM registries WHERE workspace_id=$1 AND id=$2`, ws, *a.RegistryID).Scan(&cipher); err != nil {
				return err
			}
			var access pipeline.RegistryAccess
			if err := s.openIntegration(ws, *a.RegistryID, "registry", cipher, &access); err != nil {
				return err
			}
			p.RegistryAccess[key] = access.Pull
		}
	}
	if p.Phase != "resolve" && p.Phase != "checkpoint" && p.Phase != "recovery-open" && p.Phase != "checkpoint-closed" {
		total := 0
		for _, id := range p.Unit.Plan.SecretReferences() {
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
			p.Secrets[id.String()] = string(plain)
		}
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 500000 {
		return errors.New("cross-host payload limit exceeded")
	}
	c.Crosshost = rawMessage(raw)
	return nil
}
