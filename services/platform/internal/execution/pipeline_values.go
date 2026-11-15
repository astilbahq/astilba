package execution

import (
	"context"
	json "encoding/json/v2"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// populatePipeline fills in the values of a pipeline that may start until deadline. A zero
// deadline only checks they can be had, for a copy that never leaves management.
func (s *Service) populatePipeline(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env, run uuid.UUID, deadline time.Time, p *pipeline.Command) error {
	clone, err := git.cloneAccess(ctx, tx, p.Connection.ID, run, deadline)
	if err != nil {
		return err
	}
	if clone.Err != nil {
		return clone.Err
	}
	p.Access = &clone.Value
	if p.Build != nil {
		p.BuildSecrets, err = s.buildInputs(ctx, tx, git, ws, env, *p.Build)
		if err != nil {
			return err
		}
		if p.Registry != nil {
			_, cipher, err := loadRegistry(ctx, tx, ws, p.Registry.ID)
			if err != nil {
				return err
			}
			var access pipeline.RegistryAccess
			plain, err := git.keys.inspect.open(secretbroker.Item{Kind: secretbroker.Registry, ID: p.Registry.ID}, cipher)
			if err != nil {
				return err
			}
			err = json.Unmarshal(plain, &access)
			clear(plain)
			if err != nil {
				return err
			}
			p.RegistryAccess = &pipeline.RegistryAccess{Push: access.Push}
		}
	}
	raw, err := stablejson.Marshal(p)
	if err != nil || len(raw) > 250000 {
		return domain.Invalid("pipeline.payloadBudget")
	}
	return nil
}
