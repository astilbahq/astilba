package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// River's listener takes a connection out of the pool for good and the pool opens another
// in its place, so a serving process holds one more connection than its pool size.
const connectionsOutsidePool = 1

// poolWaitWarning is the mean wait for a connection above which a minute's log line is a
// warning. Measured minutes reached 12 ms close to the pool's ceiling, and at least 58 ms
// once polls were failing.
const poolWaitWarning = 50 * time.Millisecond

// CheckConnectionLimit refuses a pool PostgreSQL could never admit, which would otherwise
// surface as "too many connections" only once the service is busy. It reads the limits,
// not current use, so it says nothing about other instances and clients sharing them.
func (s *Store) CheckConnectionLimit(ctx context.Context) error {
	available, err := s.Queries.ConnectionsAvailable(ctx)
	if err != nil {
		return fmt.Errorf("cannot read PostgreSQL's connection limit: %w", err)
	}
	size := s.Pool.Config().MaxConns
	if size+connectionsOutsidePool > available {
		return fmt.Errorf("ASTILBA_DATABASE_POOL_SIZE is %d, and with %d more for the job listener that exceeds the %d connections PostgreSQL allows this role; lower it or raise PostgreSQL's limit", size, connectionsOutsidePool, available)
	}
	return nil
}

// poolUse holds the pool's counters, cumulative or for a period.
type poolUse struct {
	acquires, waited, canceled int64
	waiting                    time.Duration
}

func readPoolUse(stat *pgxpool.Stat) poolUse {
	return poolUse{acquires: stat.AcquireCount(), waited: stat.EmptyAcquireCount(), canceled: stat.CanceledAcquireCount(), waiting: stat.EmptyAcquireWaitTime()}
}

func (u poolUse) since(earlier poolUse) poolUse {
	return poolUse{acquires: u.acquires - earlier.acquires, waited: u.waited - earlier.waited, canceled: u.canceled - earlier.canceled, waiting: u.waiting - earlier.waiting}
}

// short reports a period in which work was held up for a connection. A cancelled acquire
// alone does not show that, because a query started after its client disconnected counts
// too, however idle the pool. Only completed acquires record their wait, so a pool too
// stuck to complete any shows instead as cancellations outnumbering them.
func (u poolUse) short() bool {
	return u.waiting > poolWaitWarning*time.Duration(u.acquires) || u.canceled > u.acquires
}

// LogPoolUse logs each minute how often work had to wait for a connection, until ctx ends.
// docs/hosting.md#database-connections explains how to read the line.
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
		use := now.since(last)
		level := slog.LevelInfo
		if use.short() {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "database pool", "size", stat.MaxConns(), "open", stat.TotalConns(), "inUse", stat.AcquiredConns(), "acquires", use.acquires, "waited", use.waited, "waitSeconds", use.waiting.Seconds(), "canceled", use.canceled)
		last = now
	}
}
