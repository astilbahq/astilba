package integration

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A pool PostgreSQL could never admit would otherwise fail with "too many clients" only
// under load, so serving refuses it at startup, counting River's listener.
func TestServingRefusesAPoolPostgreSQLCannotAdmit(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	available, err := f.st.Queries.ConnectionsAvailable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if available < 1 {
		t.Fatal("connections available", available)
	}
	check := func(size int32) error {
		config := f.st.Pool.Config()
		config.MaxConns = size
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		return store.New(pool, nil, false).CheckConnectionLimit(ctx)
	}
	if err := check(available - 1); err != nil {
		t.Fatal("a pool that fits was refused: ", err)
	}
	if err := check(available); err == nil {
		t.Fatal("a pool with no room for the listener was accepted")
	}
}
