package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
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
	var sourceAttempts, accountAttempts int32
	if err := f.st.Pool.QueryRow(ctx, "SELECT (SELECT attempts FROM auth_attempts WHERE scope_hash=$1),(SELECT attempts FROM auth_attempts WHERE scope_hash=$2)", auth.Hash("password:ip:192.0.2.1"), auth.Hash("password:account:unknown@example.test")).Scan(&sourceAttempts, &accountAttempts); err != nil {
		t.Fatal(err)
	}
	if sourceAttempts != 1001 || accountAttempts != 50 {
		t.Fatalf("local refusal accounting: source %d, account %d", sourceAttempts, accountAttempts)
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

func TestLoginGlobalRefusalPreservesLocalBudgets(t *testing.T) {
	for _, scope := range []string{"account", "source"} {
		t.Run(scope, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			global := auth.Hash("password:global")
			if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,1000,now()+interval '15 minutes')", global); err != nil {
				t.Fatal(err)
			}
			retries := 6
			if scope == "source" {
				retries = 51
			}
			for i := range retries {
				email := "owner@example.test"
				if scope == "source" {
					email = fmt.Sprintf("unknown-%d@example.test", i)
				}
				if _, err := f.auth.Login(ctx, email, f.password, "192.0.2.1"); !errors.Is(err, domain.RateLimited) {
					t.Fatal("full global budget admitted a login", err)
				}
			}
			if _, err := f.st.Pool.Exec(ctx, "UPDATE auth_attempts SET expires_at=now()-interval '1 second' WHERE scope_hash=$1", global); err != nil {
				t.Fatal(err)
			}
			if _, err := f.auth.Login(ctx, "owner@example.test", f.password, "192.0.2.1"); err != nil {
				t.Fatal("global refusal spent the local budget", err)
			}
		})
	}
}

func TestLoginGlobalBudgetSerializesConcurrentAdmissions(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,999,now()+interval '15 minutes')", auth.Hash("password:global")); err != nil {
		t.Fatal(err)
	}
	// Two service instances share PostgreSQL, as management replicas do.
	replica := *f.auth
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := range cap(results) {
		service := f.auth
		if i%2 != 0 {
			service = &replica
		}
		go func() {
			<-start
			_, err := service.Login(ctx, fmt.Sprintf("unknown-%d@example.test", i), "wrong password", fmt.Sprintf("192.0.2.%d", i+1))
			results <- err
		}()
	}
	close(start)
	admitted := 0
	for range cap(results) {
		err := <-results
		if errors.Is(err, domain.Credentials) {
			admitted++
		} else if !errors.Is(err, domain.RateLimited) {
			t.Fatal("unexpected admission error", err)
		}
	}
	if admitted != 1 {
		t.Fatalf("last global slot admitted %d attempts, want 1", admitted)
	}
}

func TestLoginExpiryCleanupDoesNotBlockAdmission(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source := auth.Hash("password:ip:192.0.2.1")
	global := auth.Hash("password:global")
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,50,now()-interval '1 second'),($2,1000,now()-interval '1 second')", source, global); err != nil {
		t.Fatal(err)
	}
	locked, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(context.Background())
	var holder int32
	if err := locked.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&holder); err != nil {
		t.Fatal(err)
	}
	if _, err := locked.Exec(ctx, "SELECT 1 FROM auth_attempts WHERE scope_hash=$1 FOR UPDATE", source); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := f.auth.Login(ctx, "owner@example.test", f.password, "192.0.2.1")
		finished <- err
	}()
	// Login must finish cleanup before it waits for this source. Retaining an
	// expired global-row lock here would invert the source-to-global order.
	waiting := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%-- name: RecordAuthAttempt%')", holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("login cleanup blocked before source admission")
	}
	unblocked, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if err := f.auth.Limit(unblocked, "unrelated", 5, 15*time.Minute); err != nil {
		t.Fatal("cleanup waited for the locked expired source", err)
	}
	if _, err := db.New(locked).RecordAuthAttempt(unblocked, db.RecordAuthAttemptParams{ScopeHash: global, ExpiresAt: f.auth.Now().Add(15 * time.Minute)}); err != nil {
		t.Fatal("login cleanup retained the global lock while waiting for source", err)
	}
	if err := locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal("expired counters did not recover after contention", err)
	}
}
