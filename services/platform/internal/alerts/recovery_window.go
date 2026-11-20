package alerts

import (
	"context"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recoveryWindow raises recovery_window, for the workspace itself, while its newest recovery base
// has a short window. A later base with a longer one resolves it.
func (s *Service) recoveryWindow(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	base, err := db.New(tx).NewestRecoveryBase(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	value := good
	if portability.ShortRecoveryWindow(base.CapturedAt, base.WindowStart) {
		value = bad
	}
	return s.condition(ctx, tx, ws, api.RecoveryWindow, target{resource: ws}, value)
}
