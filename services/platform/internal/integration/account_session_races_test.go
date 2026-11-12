package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5"
)

// A logout that loaded its token just before another tab renewed it must still sign the
// browser out: on a shared machine the renewed cookie would otherwise keep the next person
// signed in. A separate sign-in of the same account is another lineage and stays.
func TestLogoutEndsARenewalThatRacedIt(t *testing.T) {
	for _, renewals := range []struct {
		name  string
		renew func(t *testing.T, f *fixture, b *browser)
	}{
		{"reauthentication", func(t *testing.T, f *fixture, b *browser) {
			b.reauthenticate(map[string]string{"password": f.password}, 200)
		}},
		{"two renewals", func(t *testing.T, f *fixture, b *browser) {
			b.reauthenticate(map[string]string{"password": f.password}, 200)
			b.reauthenticate(map[string]string{"password": f.password}, 200)
		}},
	} {
		t.Run(renewals.name, func(t *testing.T) {
			f := setup(t)
			other := f.browser(t)
			other.login("owner@example.test", f.password)
			gate, release := holdSessions(t, f)
			b := f.browser(t)
			b.login("owner@example.test", f.password)
			loaded := b.client.Jar.Cookies(mustURL(f.server.URL))

			gate.armed.Store(true)
			finish := sendHeld(t, b, gate, loaded, "POST", "/api/v1/auth/logout", nil)
			renewals.renew(t, f, b)
			renewed := b.client.Jar.Cookies(mustURL(f.server.URL))
			release()

			if result := finish(); result.status != 204 {
				t.Fatalf("logout answered %d: %s", result.status, result.body)
			}
			if session := sessionFromCookies(t, b, renewed); session.User != nil {
				t.Fatal("the token that renewed the logged-out session is still signed in")
			}
			if session := other.session(); session.User == nil {
				t.Fatal("logout ended another sign-in of the same account")
			}
		})
	}
}

// Logging out with a token that has already ended still answers 204 and ends nothing else.
func TestLogoutWithAnEndedTokenSucceeds(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	stale := b.client.Jar.Cookies(mustURL(f.server.URL))
	b.reauthenticate(map[string]string{"password": f.password}, 200)

	staleBrowser := f.browser(t)
	staleBrowser.client.Jar.SetCookies(mustURL(f.server.URL), stale)
	if session := staleBrowser.session(); session.User != nil {
		t.Fatal("the replaced token is still signed in")
	}
	staleBrowser.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	if session := b.session(); session.User == nil {
		t.Fatal("logging out an ended token ended the session that had replaced it")
	}
}

// Creating a workspace waits for any holder of the account. A logout that lands during the
// wait must stop the stolen session's workspace being created.
func TestCreateWorkspaceWaitingBehindAccountLock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	attacker := f.browser(t)
	attacker.login("owner@example.test", f.password)
	held := holdAccount(t, f)
	request := attacker.send("POST", "/api/v1/workspaces", map[string]string{"name": "Planted"})
	awaitLockWait(t, f, "name: InsertMembership")
	attacker.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	requireReply(t, <-request, 401)
	var created bool
	if err := f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspaces WHERE name='Planted')").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("a logged-out session created a workspace")
	}
}

// Accepting a hosted invitation as an existing account waits for any holder of the account.
// A logout that lands during the wait must leave the invitation unused.
func TestHostedAcceptanceWaitingBehindAccountLock(t *testing.T) {
	f, principal, _ := hostedFixture(t)
	ctx := context.Background()
	if err := f.auth.InviteHosted(ctx, "owner@example.test", f.apiServer.Origin); err != nil {
		t.Fatal(err)
	}
	token := tokenFromLinkMail(t, f, "/signup")
	held := holdAccount(t, f)
	result := make(chan error, 1)
	go func() {
		_, _, err := f.auth.AcceptHosted(ctx, token, "", "", "Planted", auth.Source{Key: "test", Network: "test"}, &principal)
		result <- err
	}()
	awaitLockWait(t, f, "name: MarkEmailVerified")
	if err := endSession(ctx, f, principal.SessionHash); err != nil {
		t.Fatal(err)
	}
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-result; !errors.Is(err, domain.Unauthenticated) {
		t.Fatal("a logged-out session accepted a hosted invitation: ", err)
	}
	var created bool
	if err := f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM workspaces WHERE name='Planted')").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("the refused acceptance created a workspace")
	}
	if err := f.auth.ValidateHostedInvitation(ctx, token, "test"); err != nil {
		t.Fatal("the refused acceptance used the invitation: ", err)
	}
}

// An import waits for the account lock after opening its archive. Recent authentication that
// lapses meanwhile no longer counts.
func TestImportRecencyLapsesBehindAccountLock(t *testing.T) {
	ctx := context.Background()
	const passphrase = "unique archive passphrase for fixture"
	source, exporter, ws := sessionFixture(t)
	archive, err := (&portability.Service{Store: source.st}).Export(ctx, exporter, ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}

	f, principal, _ := sessionFixture(t)
	principal.RecentUntil = time.Now().Add(2 * time.Second)
	held := holdAccount(t, f)
	result := make(chan error, 1)
	go func() {
		_, err := (&portability.Service{Store: f.st}).Import(ctx, principal, passphrase, archive, "")
		result <- err
	}()
	awaitLockWait(t, f, "name: LockAccount")
	time.Sleep(time.Until(principal.RecentUntil))
	if err := held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, domain.RecentRequired) {
		t.Fatal("lapsed authentication imported a workspace: ", err)
	}
}

// A change to the account's own credentials checks the shorter recent window before it
// hashes a password or waits for the account. A window that lapses meanwhile no longer counts.
func TestCredentialRecencyLapsesBehindAccountLock(t *testing.T) {
	for _, change := range []struct {
		name string
		mfa  bool
		// prepare runs before the account is held and returns the change.
		prepare func(t *testing.T, ctx context.Context, f *fixture, p store.Principal) func() error
	}{
		{"change password", false, func(t *testing.T, ctx context.Context, f *fixture, p store.Principal) func() error {
			return func() error {
				_, err := f.auth.ChangePassword(ctx, p, "replacement password here")
				return err
			}
		}},
		{"confirm MFA", false, func(t *testing.T, ctx context.Context, f *fixture, p store.Principal) func() error {
			secret, _, err := f.auth.BeginMFA(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			return func() error {
				_, _, err := f.auth.ConfirmMFA(ctx, p, codeAt(t, secret, time.Now()))
				return err
			}
		}},
		{"disable MFA", true, func(t *testing.T, ctx context.Context, f *fixture, p store.Principal) func() error {
			return func() error {
				_, err := f.auth.DisableMFA(ctx, p)
				return err
			}
		}},
		{"regenerate recovery codes", true, func(t *testing.T, ctx context.Context, f *fixture, p store.Principal) func() error {
			return func() error {
				_, _, err := f.auth.RegenerateRecovery(ctx, p)
				return err
			}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if change.mfa {
				enableMFA(t, f.browser(t).withLogin("owner@example.test", f.password))
			}
			account, err := f.st.Queries.AccountByID(ctx, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			principal := browserPrincipal(t, f.st, account, change.mfa)
			principal.CredentialRecentUntil = time.Now().Add(2 * time.Second)
			run := change.prepare(t, ctx, f, principal)
			before := accountCredentials(t, f)
			held := holdAccount(t, f)
			result := make(chan error, 1)
			go func() { result <- run() }()
			awaitLockWait(t, f, "name: LockAccount")
			time.Sleep(time.Until(principal.CredentialRecentUntil))
			if err := held.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, domain.RecentRequired) {
				t.Fatal("lapsed authentication changed the account's credentials: ", err)
			}
			if after := accountCredentials(t, f); after != before {
				t.Fatalf("a lapsed session's change committed:\n%s\n%s", before, after)
			}
		})
	}
}

// holdAccount locks the fixture owner's account row until the returned transaction ends.
func holdAccount(t *testing.T, f *fixture) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	held, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback(ctx) })
	if _, err = held.Exec(ctx, "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", f.owner); err != nil {
		t.Fatal(err)
	}
	return held
}

func (b *browser) withLogin(email, password string) *browser {
	b.login(email, password)
	return b
}
