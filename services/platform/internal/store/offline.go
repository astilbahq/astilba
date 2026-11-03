package store

import (
	"context"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const recoveryProcessLock int64 = 0x415354494c4241

// ConfigureServingPool fences every serving database session. Surviving sessions
// retain exclusion after another connection dies; reconnecting sessions cannot
// bypass an offline recovery transaction. This requires session-preserving
// PostgreSQL connections, as used by the controller's River workers.
func ConfigureServingPool(config *pgxpool.Config) {
	prior := config.AfterConnect
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if prior != nil {
			if err := prior(ctx, conn); err != nil {
				return err
			}
		}
		acquired, err := db.New(conn).TakeRecoveryProcessSharedLock(ctx, recoveryProcessLock)
		if err != nil {
			return err
		}
		if !acquired {
			return errors.New("offline recovery currently excludes serving database sessions")
		}
		return nil
	}
}

func RequireOffline(ctx context.Context, tx pgx.Tx) error {
	acquired, err := db.New(tx).TakeRecoveryProcessLock(ctx, recoveryProcessLock)
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("stop every management HTTP/worker process before recovery")
	}
	return nil
}
