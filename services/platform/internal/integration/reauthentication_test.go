package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/pquerna/otp/totp"
)

// staleOwner signs the owner in, optionally with two-factor authentication, then moves the
// clock past recent authentication. It returns the browser, its workspace and the
// authenticator secret, if any.
func staleOwner(t *testing.T, f *fixture, mfa bool) (*browser, api.Workspace, string, api.RecoveryCodes) {
	t.Helper()
	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	var secret string
	var codes api.RecoveryCodes
	if mfa {
		secret = enrollSecret(t, b)
		code, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		b.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
		b.csrf = codes.Session.CsrfToken
	}
	later := time.Now().Add(6 * time.Minute)
	f.auth.Now = func() time.Time { return later }
	if b.session().RecentAuth {
		t.Fatal("the session stayed recent past the window")
	}
	return b, session.Workspaces[0], secret, codes
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (b *browser) saveWorkspace(ws api.Workspace, status int) {
	b.t.Helper()
	b.call("PUT", "/api/v1/workspaces/"+ws.Id.String(), map[string]any{"name": ws.Name, "requireMfa": false}, status, nil)
}

func (b *browser) reauthenticate(body map[string]string, status int) api.Session {
	b.t.Helper()
	var session api.Session
	b.call("POST", "/api/v1/auth/reauthenticate", body, status, &session)
	if status == 200 {
		b.csrf = session.CsrfToken
	}
	return session
}

// With two-factor authentication a fresh code alone confirms identity: the session proved the
// password when it signed in. The password is neither needed nor accepted.
func TestReauthenticationWithTwoFactorTakesTheCodeAlone(t *testing.T) {
	f := setup(t)
	b, ws, secret, _ := staleOwner(t, f, true)
	now := f.auth.Now()
	b.saveWorkspace(ws, 403)
	b.reauthenticate(map[string]string{"password": f.password}, 400)
	b.reauthenticate(map[string]string{"password": f.password, "code": codeAt(t, secret, now)}, 400)
	b.reauthenticate(map[string]string{"code": "000000"}, 400)
	b.saveWorkspace(ws, 403)
	if session := b.reauthenticate(map[string]string{"code": codeAt(t, secret, now)}, 200); !session.RecentAuth {
		t.Fatal("a code did not renew recent authentication")
	}
	b.saveWorkspace(ws, 200)
	// The window is measured from the confirmation, not from sign-in.
	f.auth.Now = func() time.Time { return now.Add(6 * time.Minute) }
	b.saveWorkspace(ws, 403)
}

// A time step is accepted once, as at sign-in, so a code someone watched being typed
// cannot confirm another session.
func TestReauthenticationRefusesAReplayedCode(t *testing.T) {
	f := setup(t)
	b, _, secret, _ := staleOwner(t, f, true)
	code := codeAt(t, secret, f.auth.Now())
	b.reauthenticate(map[string]string{"code": code}, 200)
	other := f.browser(t)
	var pending api.Session
	other.call("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.test", "password": f.password}, 200, &pending)
	other.csrf = pending.CsrfToken
	other.call("POST", "/api/v1/auth/mfa", map[string]string{"code": code}, 400, nil)
	b.reauthenticate(map[string]string{"code": code}, 400)
}

// A recovery code is consumed exactly as at sign-in: it works once, and its use ends every
// other session of the account, while the session that used it carries on.
func TestReauthenticationConsumesARecoveryCode(t *testing.T) {
	f := setup(t)
	b, ws, secret, codes := staleOwner(t, f, true)
	other := f.browser(t)
	var pending api.Session
	other.call("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.test", "password": f.password}, 200, &pending)
	other.csrf = pending.CsrfToken
	var signedIn api.Session
	other.call("POST", "/api/v1/auth/mfa", map[string]string{"code": codeAt(t, secret, f.auth.Now())}, 200, &signedIn)
	other.csrf = signedIn.CsrfToken
	session := b.reauthenticate(map[string]string{"code": codes.Codes[0]}, 200)
	if session.User == nil || session.User.RecoveryCodesRemaining != len(codes.Codes)-1 {
		t.Fatal("the recovery code was not consumed", session.User)
	}
	b.saveWorkspace(ws, 200)
	if other.session().User != nil {
		t.Fatal("a recovery code left another session signed in")
	}
	b.reauthenticate(map[string]string{"code": codes.Codes[0]}, 400)
}

// Without two-factor authentication the password is the step-up, and a code is refused
// rather than ignored.
func TestReauthenticationWithoutTwoFactorTakesThePassword(t *testing.T) {
	f := setup(t)
	b, ws, _, _ := staleOwner(t, f, false)
	b.reauthenticate(map[string]string{"code": "123456"}, 400)
	b.reauthenticate(map[string]string{"password": "not the password at all"}, 400)
	b.saveWorkspace(ws, 403)
	if session := b.reauthenticate(map[string]string{"password": f.password}, 200); !session.RecentAuth {
		t.Fatal("the password did not renew recent authentication")
	}
	b.saveWorkspace(ws, 200)
}

// Five attempts in a window, successful or not, and then nothing, even a right code.
func TestReauthenticationAttemptsAreLimited(t *testing.T) {
	f := setup(t)
	b, _, secret, _ := staleOwner(t, f, true)
	for range 5 {
		b.reauthenticate(map[string]string{"code": "000000"}, 400)
	}
	b.reauthenticate(map[string]string{"code": codeAt(t, secret, f.auth.Now())}, 429)
	if b.session().RecentAuth {
		t.Fatal("a refused attempt renewed recent authentication")
	}
}

// Failures also spend a daily allowance, which bounds guessing at a code by anyone holding a
// session. Successes do not spend it, so confirming often never locks the owner out.
func TestReauthenticationFailuresSpendADailyAllowance(t *testing.T) {
	f := setup(t)
	b, _, secret, _ := staleOwner(t, f, true)
	ctx := context.Background()
	freshWindow := func() {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, "DELETE FROM auth_attempts WHERE scope_hash=$1", auth.Hash(fmt.Sprintf("reauth:%s", f.owner))); err != nil {
			t.Fatal(err)
		}
	}
	failures := func() (count int) {
		t.Helper()
		_ = f.st.Pool.QueryRow(ctx, "SELECT attempts FROM auth_attempts WHERE scope_hash=$1", auth.Hash(fmt.Sprintf("reauth-failures:%s", f.owner))).Scan(&count)
		return count
	}
	now := f.auth.Now()
	b.reauthenticate(map[string]string{"code": codeAt(t, secret, now)}, 200)
	if failures() != 0 {
		t.Fatal("a success spent the daily allowance")
	}
	for range 4 {
		freshWindow()
		for range 5 {
			b.reauthenticate(map[string]string{"code": "000000"}, 400)
		}
	}
	if failures() != 20 {
		t.Fatal("failures were not counted", failures())
	}
	freshWindow()
	b.reauthenticate(map[string]string{"code": codeAt(t, secret, now.Add(time.Minute))}, 429)
}

// A session the account has moved past cannot confirm itself back to life, even with a
// right code. Over HTTP its token no longer passes the request checks; this drives the
// service with a session that was current when its request began, as one racing another
// session's password change would be.
func TestReauthenticationCannotRestoreAnEndedSession(t *testing.T) {
	f := setup(t)
	_, _, secret, _ := staleOwner(t, f, true)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	principal := browserPrincipal(t, f.st, account, true)
	if err := f.st.Queries.BumpAuthVersion(ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.Reauthenticate(ctx, principal, "", codeAt(t, secret, f.auth.Now())); !errors.Is(err, domain.Unauthenticated) {
		t.Fatal("an ended session was confirmed", err)
	}
}

// Concurrent guesses cannot overrun the daily allowance between them: each is charged a
// failure before its code is checked.
func TestReauthenticationConcurrentGuessesStayWithinTheDailyAllowance(t *testing.T) {
	f := setup(t)
	staleOwner(t, f, true)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	principal := browserPrincipal(t, f.st, account, true)
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,19,now()+interval '1 day')", auth.Hash(fmt.Sprintf("reauth-failures:%s", f.owner))); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := f.auth.Reauthenticate(ctx, principal, "", "000000")
			results <- err
		}()
	}
	checked := 0
	for range 5 {
		switch err := <-results; {
		case errors.Is(err, domain.Credentials):
			checked++
		case !errors.Is(err, domain.RateLimited):
			t.Fatal("unexpected refusal", err)
		}
	}
	if checked != 1 {
		t.Fatalf("%d guesses were checked with one failure left in the allowance", checked)
	}
}
