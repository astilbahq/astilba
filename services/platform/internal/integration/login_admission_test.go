package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

func TestLoginGlobalBudgetCountsAdmittedAttempts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 1001; i++ {
		_, err := f.auth.Login(ctx, "unknown@example.test", "wrong password", "192.0.2.1")
		if !errors.Is(err, domain.Credentials) && !errors.Is(err, domain.RateLimited) {
			t.Fatal(err)
		}
	}
	if _, err := f.auth.Login(ctx, "owner@example.test", f.password, "192.0.2.2"); err != nil {
		t.Fatal("throttled client exhausted another client's admission", err)
	}
	// Exercise the final global admission without computing 1,000 password hashes.
	if _, err := f.st.Pool.Exec(ctx, "UPDATE auth_attempts SET attempts=999 WHERE scope_hash=$1", auth.Hash("password:global")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.Login(ctx, "owner@example.test", f.password, "192.0.2.3"); err != nil {
		t.Fatal("last global admission was unavailable", err)
	}
	if _, err := f.auth.Login(ctx, "owner@example.test", f.password, "192.0.2.4"); !errors.Is(err, domain.RateLimited) {
		t.Fatal("admitted clients bypassed the global budget", err)
	}
}
