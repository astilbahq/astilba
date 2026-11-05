package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// holdWorkspaceRow takes the row lock as another management process would, because waiting
// for the workspace turn cannot be seen from outside. The function it returns blocks until
// a request is waiting for that lock.
func holdWorkspaceRow(t *testing.T, f *fixture, ws uuid.UUID) (pgx.Tx, func()) {
	t.Helper()
	ctx := context.Background()
	held, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback(ctx) })
	if _, err = held.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR NO KEY UPDATE", ws); err != nil {
		t.Fatal(err)
	}
	return held, func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var waiting bool
			if err := f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%name: LockWorkspaceByID%')").Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the request never reached the workspace lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func changePassword(t *testing.T, ctx context.Context, f *fixture) error {
	t.Helper()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.ChangePassword(ctx, f.owner, account.AuthVersion, "replacement password here")
	return err
}

// A stolen session's write can wait seconds behind an export. Whatever the owner did to
// end that session while it waited must stop it committing.
func TestBrowserWriteWaitingBehindWorkspaceLock(t *testing.T) {
	ctx := context.Background()
	revocations := []struct {
		name   string
		mfa    bool
		revoke func(t *testing.T, f *fixture, attacker *browser)
	}{
		{name: "password change", revoke: func(t *testing.T, f *fixture, _ *browser) {
			if err := changePassword(t, ctx, f); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "MFA disabled", mfa: true, revoke: func(t *testing.T, f *fixture, _ *browser) {
			account, err := f.st.Queries.AccountByID(ctx, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.auth.DisableMFA(ctx, f.owner, account.AuthVersion); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "logout", revoke: func(t *testing.T, _ *fixture, attacker *browser) {
			// The attacker holds the same cookie, so the owner's logout ends this session.
			attacker.call("POST", "/api/v1/auth/logout", nil, 204, nil)
		}},
		{name: "identity confirmation", revoke: func(t *testing.T, f *fixture, attacker *browser) {
			// The owner keeps the session under a new token, and a stolen copy of the old one dies.
			attacker.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, nil)
		}},
		{name: "session expiry", revoke: func(t *testing.T, f *fixture, _ *browser) {
			if _, err := f.st.Pool.Exec(ctx, "UPDATE sessions SET expiry=clock_timestamp()"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, revocation := range revocations {
		t.Run(revocation.name, func(t *testing.T) {
			f := setup(t)
			attacker := f.browser(t)
			ws := attacker.login("owner@example.test", f.password).Workspaces[0].Id
			if revocation.mfa {
				enableMFA(t, attacker)
			}
			audits := func() (n int) {
				t.Helper()
				if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE workspace_id=$1", ws).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := audits()

			held, reached := holdWorkspaceRow(t, f, ws)
			write := attacker.send("PUT", "/api/v1/workspaces/"+ws.String(), map[string]any{"name": "Attacker", "requireMfa": false})
			reached()
			revocation.revoke(t, f, attacker)
			if err := held.Rollback(ctx); err != nil {
				t.Fatal(err)
			}

			requireReply(t, <-write, 401)
			var name string
			if err := f.st.Pool.QueryRow(ctx, "SELECT name FROM workspaces WHERE id=$1", ws).Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name == "Attacker" {
				t.Fatal("a revoked session's write committed")
			}
			if after := audits(); after != before {
				t.Fatalf("a refused write left %d audit records", after-before)
			}
		})
	}

	// The same interleaving with nothing revoked, which also proves a real session's stored
	// token reaches the read.
	t.Run("still valid", func(t *testing.T) {
		f := setup(t)
		owner := f.browser(t)
		ws := owner.login("owner@example.test", f.password).Workspaces[0].Id
		held, reached := holdWorkspaceRow(t, f, ws)
		write := owner.send("PUT", "/api/v1/workspaces/"+ws.String(), map[string]any{"name": "Renamed", "requireMfa": false})
		reached()
		if err := held.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		requireReply(t, <-write, 200)
	})
}

// An invitation is also a browser's write behind the lock, and it does not pass through
// the admission other writes share.
func TestInvitationWaitingBehindWorkspaceLockAfterLogout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	attacker := f.browser(t)
	attacker.login("owner@example.test", f.password)
	token := seedInvitation(t, f, "owner@example.test")
	var ws uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT workspace_id FROM invitations WHERE email='owner@example.test'").Scan(&ws); err != nil {
		t.Fatal(err)
	}
	held, reached := holdWorkspaceRow(t, f, ws)
	accept := attacker.send("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token})
	reached()
	attacker.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	requireReply(t, <-accept, 401)
	var members int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM memberships WHERE workspace_id=$1 AND account_id=$2", ws, f.owner).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Fatal("a logged-out session accepted the invitation")
	}
}

// sessionFixture is a signed-in owner of a workspace, for tests of the store's admission.
func sessionFixture(t *testing.T) (*fixture, store.Principal, uuid.UUID) {
	t.Helper()
	f := setup(t)
	owner := f.browser(t)
	ws := owner.login("owner@example.test", f.password).Workspaces[0].Id
	account, err := f.st.Queries.AccountByID(context.Background(), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	return f, browserPrincipal(t, f.st, account, false), ws
}

// Authentication that lapses during the wait no longer counts, and nothing else bounds the
// wait: a handler outlives the server's write timeout.
func TestRecentAuthenticationLapsesBehindWorkspaceLock(t *testing.T) {
	f, principal, ws := sessionFixture(t)
	ctx := context.Background()
	principal.RecentUntil = time.Now().Add(250 * time.Millisecond)
	held, reached := holdWorkspaceRow(t, f, ws)
	result := make(chan error, 1)
	go func() {
		result <- f.st.InWorkspace(ctx, principal, ws, store.WorkspaceAccess{Write: true, Recent: true}, func(*db.Queries, store.Workspace) error { return nil })
	}()
	reached()
	time.Sleep(time.Until(principal.RecentUntil))
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, domain.RecentRequired) {
		t.Fatal("lapsed authentication was admitted: ", err)
	}
}

// An export's snapshot predates its wait for the lock, so reading the account normally would
// not see what happened during the wait.
func TestExportWaitingBehindWorkspaceLock(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		during func(t *testing.T, f *fixture, p store.Principal)
		want   error
	}{
		{"password change", func(t *testing.T, f *fixture, _ store.Principal) {
			if err := changePassword(t, ctx, f); err != nil {
				t.Fatal(err)
			}
		}, domain.Conflict},
		{"logout", func(t *testing.T, f *fixture, p store.Principal) {
			if _, err := f.st.Queries.DeleteRenewedSession(ctx, p.SessionHash); err != nil {
				t.Fatal(err)
			}
		}, domain.Conflict},
		{"idle refresh", func(t *testing.T, f *fixture, p store.Principal) {
			if _, err := f.st.Queries.RefreshSession(ctx, db.RefreshSessionParams{Token: p.SessionHash, Data: []byte{}, Expiry: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, principal, ws := sessionFixture(t)
			held, reached := holdWorkspaceRow(t, f, ws)
			result := make(chan error, 1)
			exported := false
			go func() {
				result <- f.st.InWorkspaceSnapshot(ctx, principal, ws, func(pgx.Tx, *db.Queries, store.Workspace) error {
					exported = true
					return nil
				})
			}()
			reached()
			c.during(t, f, principal)
			if err := held.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, c.want) {
				t.Fatalf("export returned %v, want %v", err, c.want)
			}
			if exported != (c.want == nil) {
				t.Fatalf("export ran: %t", exported)
			}
		})
	}

	t.Run("revoked before waiting", func(t *testing.T) {
		f, principal, ws := sessionFixture(t)
		if err := changePassword(t, ctx, f); err != nil {
			t.Fatal(err)
		}
		err := f.st.InWorkspaceSnapshot(ctx, principal, ws, func(pgx.Tx, *db.Queries, store.Workspace) error {
			t.Error("export ran")
			return nil
		})
		if !errors.Is(err, domain.Unauthenticated) {
			t.Fatal("revoked session was admitted: ", err)
		}
	})
}

// A stolen session must not be able to hold up the owner's response to the theft.
func TestRevocationDoesNotWaitForARunningExport(t *testing.T) {
	f, principal, ws := sessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	running, finish, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- f.st.InWorkspaceSnapshot(ctx, principal, ws, func(pgx.Tx, *db.Queries, store.Workspace) error {
			close(running)
			<-finish
			return nil
		})
	}()
	select {
	case <-running:
	case err := <-result:
		t.Fatal("export was not admitted: ", err)
	}
	bounded, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	passwordErr := changePassword(t, bounded, f)
	_, logoutErr := f.st.Queries.DeleteRenewedSession(bounded, principal.SessionHash)
	close(finish)
	if err := errors.Join(passwordErr, logoutErr); err != nil {
		t.Fatal("revocation waited for the export: ", err)
	}
	// Admitted before the revocation, it still finishes.
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
