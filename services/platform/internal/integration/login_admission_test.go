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

func source(address string) auth.Source { return auth.Source{Key: address, Network: address} }

func seedAttempts(t *testing.T, f *fixture, scope string, attempts int) {
	t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,$2,now()+interval '15 minutes')", auth.Hash(scope), attempts); err != nil {
		t.Fatal(err)
	}
}

func attemptCount(t *testing.T, f *fixture, scope string) int32 {
	t.Helper()
	var attempts int32
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT COALESCE((SELECT attempts FROM auth_attempts WHERE scope_hash=$1 AND expires_at>now()),0)", auth.Hash(scope)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func TestLoginRefusalChargesNoBudget(t *testing.T) {
	const email = "owner@example.test"
	for _, test := range []struct{ name, spent string }{
		{"account", "password:account:" + email},
		{"account from this source", "password:account:" + email + ":source:192.0.2.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			limit := 5
			if test.name == "account" {
				limit = 10
			}
			seedAttempts(t, f, test.spent, limit)
			for range 3 {
				if _, err := f.auth.Login(ctx, email, f.password, source("192.0.2.1"), nil); !errors.Is(err, domain.RateLimited) {
					t.Fatal("spent budget admitted a login", err)
				}
			}
			if got := attemptCount(t, f, "password:ip:192.0.2.1"); got != 0 {
				t.Fatalf("refusals charged the source %d times", got)
			}
			if got := attemptCount(t, f, test.spent); int(got) != limit {
				t.Fatalf("refusals raised the spent budget to %d", got)
			}
		})
	}
}

func TestLoginBudgetIsSharedByConcurrentReplicas(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedAttempts(t, f, "password:account:unknown@example.test", 9)
	// Two service instances share PostgreSQL, as management replicas do, but not hash slots.
	passwords, err := auth.NewPasswords()
	if err != nil {
		t.Fatal(err)
	}
	replica := *f.auth
	replica.Passwords = passwords
	start := make(chan struct{})
	results := make(chan error, 4)
	for i := range cap(results) {
		service := f.auth
		if i%2 != 0 {
			service = &replica
		}
		go func() {
			<-start
			_, err := service.Login(ctx, "unknown@example.test", "wrong password", source(fmt.Sprintf("192.0.2.%d", i+1)), nil)
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
		t.Fatalf("last attempt in the budget admitted %d logins, want 1", admitted)
	}
}

func TestLoginExpiryCleanupDoesNotBlockAdmission(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sourceScope := auth.Hash("password:ip:192.0.2.1")
	account := auth.Hash("password:account:owner@example.test")
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,50,now()-interval '1 second'),($2,10,now()-interval '1 second')", sourceScope, account); err != nil {
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
	if _, err := locked.Exec(ctx, "SELECT 1 FROM auth_attempts WHERE scope_hash=$1 FOR UPDATE", sourceScope); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := f.auth.Login(ctx, "owner@example.test", f.password, source("192.0.2.1"), nil)
		finished <- err
	}()
	// Login must finish cleanup before it waits for this source. Retaining an expired
	// account-row lock here would invert the source-to-account order.
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
	if _, err := db.New(locked).RecordAuthAttempt(unblocked, db.RecordAuthAttemptParams{ScopeHash: account, ExpiresAt: f.auth.Now().Add(15 * time.Minute)}); err != nil {
		t.Fatal("login cleanup retained the account lock while waiting for source", err)
	}
	if err := locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal("expired counters did not recover after contention", err)
	}
}
