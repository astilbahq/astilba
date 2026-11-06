package integration

import (
	"context"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A pool PostgreSQL could never admit would otherwise fail with "too many connections" only
// under load, so serving refuses it at startup, counting River's listener.
func TestServingRefusesAPoolPostgreSQLCannotAdmit(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	check := func(role string, size int32) error {
		config := f.st.Pool.Config()
		config.MaxConns = size
		if role != "" {
			config.ConnConfig.User, config.ConnConfig.Password = role, "limit-test"
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		return store.New(pool, nil, false).CheckConnectionLimit(ctx)
	}

	available, err := f.st.Queries.ConnectionsAvailable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := check("", available-1); err != nil {
		t.Fatal("a pool that fits was refused: ", err)
	}
	if err := check("", available); err == nil {
		t.Fatal("a pool with no room for the listener was accepted")
	}

	for _, role := range []string{"limit_test_ordinary", "limit_test_super"} {
		t.Cleanup(func() {
			if _, err := f.st.Pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+role); err != nil {
				t.Error(err)
			}
		})
	}
	if _, err := f.st.Pool.Exec(ctx, "CREATE ROLE limit_test_ordinary LOGIN PASSWORD 'limit-test' CONNECTION LIMIT 8"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "CREATE ROLE limit_test_super SUPERUSER LOGIN PASSWORD 'limit-test' CONNECTION LIMIT 8"); err != nil {
		t.Fatal(err)
	}
	if err := check("limit_test_ordinary", 7); err != nil {
		t.Fatal("a pool within the role's limit was refused: ", err)
	}
	if err := check("limit_test_ordinary", 8); err == nil {
		t.Fatal("a pool past the role's limit was accepted")
	}
	if err := check("limit_test_super", 8); err != nil {
		t.Fatal("a superuser, whom the limit does not bind, was refused: ", err)
	}
}
