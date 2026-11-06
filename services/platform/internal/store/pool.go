package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

// River's listener takes a connection out of the pool for good and the pool opens another
// in its place, so a serving process holds one more connection than its pool size.
const connectionsOutsidePool = 1

// CheckConnectionLimit refuses a pool PostgreSQL could never admit, which would otherwise
// surface as "too many clients" only once the service is busy. It reads the server's limit,
// not its current use, so it says nothing about other instances and clients sharing it.
func (s *Store) CheckConnectionLimit(ctx context.Context) error {
	available, err := db.New(s.Pool).ConnectionsAvailable(ctx)
	if err != nil {
		return fmt.Errorf("cannot read PostgreSQL's connection limit: %w", err)
	}
	size := s.Pool.Config().MaxConns
	if size+connectionsOutsidePool > available {
		return fmt.Errorf("ASTILBA_DATABASE_POOL_SIZE is %d, and with %d more for the job listener that exceeds the %d connections PostgreSQL allows this role; lower it or raise max_connections", size, connectionsOutsidePool, available)
	}
	return nil
}

// poolUse holds the pool's cumulative counters.
type poolUse struct {
	acquires, waited, canceled int64
	waiting                    time.Duration
}

func readPoolUse(stat *pgxpool.Stat) poolUse {
	return poolUse{acquires: stat.AcquireCount(), waited: stat.EmptyAcquireCount(), canceled: stat.CanceledAcquireCount(), waiting: stat.EmptyAcquireWaitTime()}
}

// LogPoolUse logs each minute how often work had to wait for a connection, until ctx ends.
// docs/hosting.md#database-connections explains how to read the line. A cancelled acquire
// is a warning because the caller gave up waiting: every measured run past the pool's
// ceiling had them, and none had them with room to spare.
func (s *Store) LogPoolUse(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	last := readPoolUse(s.Pool.Stat())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stat := s.Pool.Stat()
		now := readPoolUse(stat)
		canceled := now.canceled - last.canceled
		level := slog.LevelInfo
		if canceled > 0 {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "database pool", "size", stat.MaxConns(), "open", stat.TotalConns(), "inUse", stat.AcquiredConns(), "acquires", now.acquires-last.acquires, "waited", now.waited-last.waited, "waitSeconds", (now.waiting - last.waiting).Seconds(), "canceled", canceled)
		last = now
	}
}
