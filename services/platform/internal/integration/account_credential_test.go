package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/jackc/pgx/v5"
)

// mfaOwner turns two-factor authentication on for the owner from a signed-in browser, and
// returns the authenticator secret and the recovery codes. The authenticator's current time
// step is spent.
func mfaOwner(t *testing.T, f *fixture, b *browser) (string, api.RecoveryCodes) {
	t.Helper()
	secret := enrollSecret(t, b)
	var codes api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, time.Now()), "current": map[string]string{"password": f.password}}, 200, &codes)
	b.csrf = codes.Session.CsrfToken
	awaitNotice(t, f, "Two-factor authentication is on")
	return secret, codes
}

// awaitNotice waits for account security notices with these subjects, in any order, since
// notices of one change are sent concurrently. Each must go to the owner and send them to this
// installation's reset page.
func awaitNotice(t *testing.T, f *fixture, subjects ...string) {
	t.Helper()
	want := map[string]int{}
	for _, subject := range subjects {
		want[subject]++
	}
	for range subjects {
		select {
		case notice := <-f.notices:
			subject, _, _ := strings.Cut(strings.SplitN(notice, "Subject: ", 2)[1], "\n")
			if want[subject] == 0 || !strings.Contains(notice, "To: owner@example.test") || !strings.Contains(notice, f.server.URL+"/reset ") {
				t.Fatalf("expected notices %q to the owner, got:\n%s", subjects, notice)
			}
			want[subject]--
		case <-time.After(15 * time.Second):
			t.Fatalf("missing notices among %q", subjects)
		}
	}
}

// signInCredentials is how the owner's account signs in: what the account's own changes alter.
func signInCredentials(t *testing.T, f *fixture) string {
	t.Helper()
	var state string
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT password_hash || ' ' || (totp_secret IS NOT NULL)::text || ' ' || auth_version::text FROM accounts WHERE id=$1`, f.owner).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// clearBudgets forgets every attempt so far, so one test can try each change in turn.
func clearBudgets(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), "DELETE FROM auth_attempts"); err != nil {
		t.Fatal(err)
	}
}

func securityEvents(t *testing.T, f *fixture) []string {
	t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), "SELECT kind FROM account_security_events WHERE account_id=$1 ORDER BY created_at, kind", f.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var failure api.Error
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("not an error: %s", body)
	}
	return failure.Code
}

// Whoever holds a recently confirmed session, but not the account's credentials, cannot take
// the account over: not by changing the password, and not by enrolling an authenticator that
// would outlast the owner's password reset.
func TestStolenSessionCannotTakeOverTheAccount(t *testing.T) {
	f := setup(t)
	thief := f.browser(t)
	thief.login("owner@example.test", f.password)
	if !thief.session().RecentAuth {
		t.Fatal("expected a recently confirmed session")
	}
	before := signInCredentials(t, f)

	if code := errorCode(t, thief.call("POST", "/api/v1/auth/password", map[string]string{"password": "the thief's new password"}, 400, nil)); code != "invalid_input" {
		t.Fatal("a password change without the current password answered ", code)
	}
	wrong := map[string]string{"password": "a guess at the password"}
	if code := errorCode(t, thief.call("POST", "/api/v1/auth/password", map[string]any{"password": "the thief's new password", "current": wrong}, 400, nil)); code != "invalid_credentials" {
		t.Fatal("a password change with a wrong password answered ", code)
	}
	secret := enrollSecret(t, thief)
	thief.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, time.Now())}, 400, nil)
	thief.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, time.Now()), "current": wrong}, 400, nil)

	if after := signInCredentials(t, f); after != before || len(securityEvents(t, f)) != 0 {
		t.Fatalf("the stolen session changed how the account signs in:\n%s\n%s", before, after)
	}
	owner := f.browser(t)
	if session := owner.login("owner@example.test", f.password); session.User == nil || session.MfaPending {
		t.Fatal("the owner can no longer sign in with the password alone")
	}
}

// Each change to the password or two-factor authentication takes the account's strongest
// current credential, refuses anything else, and tells the account holder by email.
func TestCredentialChangesTakeTheCurrentCredentialAndNotify(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)

	// Without two-factor authentication the password is the current credential. Another kind is
	// refused on `current`, apart from the change's own fields, so the dashboard can ask again.
	var session api.Session
	wrongKind := func(body []byte) {
		t.Helper()
		var failure api.Error
		if err := json.Unmarshal(body, &failure); err != nil || failure.Code != "invalid_input" || failure.Fields == nil || len(*failure.Fields) != 1 || (*failure.Fields)[0] != "current" {
			t.Fatalf("a credential of the wrong kind was not refused on current: %s", body)
		}
	}
	wrongKind(b.call("POST", "/api/v1/auth/password", map[string]any{"password": "a second password here", "current": map[string]string{"code": "123456"}}, 400, nil))
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": "a second password here", "current": map[string]string{"password": f.password}}, 200, &session)
	b.csrf = session.CsrfToken
	awaitNotice(t, f, "Your Astilba password was changed")
	f.password = "a second password here"

	secret, codes := mfaOwner(t, f, b)
	clearBudgets(t, f)

	// With it, a code is, and the password is refused.
	wrongKind(b.call("POST", "/api/v1/auth/mfa/recovery-codes", map[string]any{"current": map[string]string{"password": f.password}}, 400, nil))
	b.call("POST", "/api/v1/auth/mfa/recovery-codes", map[string]any{"current": map[string]string{"code": "000000"}}, 400, nil)
	var replaced api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/recovery-codes", map[string]any{"current": map[string]string{"code": codeAt(t, secret, time.Now().Add(30*time.Second))}}, 200, &replaced)
	b.csrf = replaced.Session.CsrfToken
	awaitNotice(t, f, "New Astilba recovery codes")
	clearBudgets(t, f)
	b.call("POST", "/api/v1/auth/mfa/disable", map[string]any{"current": map[string]string{"code": codes.Codes[0]}}, 400, nil)

	// A recovery code is a current credential too, and its use is a notice of its own.
	b.call("POST", "/api/v1/auth/mfa/disable", map[string]any{"current": map[string]string{"code": replaced.Codes[0]}}, 200, &session)
	b.csrf = session.CsrfToken
	awaitNotice(t, f, "An Astilba recovery code was used", "Two-factor authentication is off")

	want := []string{auth.PasswordChanged, auth.MFAEnabled, auth.RecoveryCodesRegenerated, auth.RecoveryCodeUsed, auth.MFADisabled}
	if got := securityEvents(t, f); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("account security events %v, want %v", got, want)
	}
}

// Confirming identity and completing a reset, which change how the account signs in without a
// session's request for it, notify the account holder too.
func TestRecoveryCodeSignInAndResetNotify(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	_, codes := mfaOwner(t, f, b)

	other := f.browser(t)
	if pending := other.login("owner@example.test", f.password); !pending.MfaPending {
		t.Fatal("expected a second factor")
	}
	other.call("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[0]}, 200, nil)
	awaitNotice(t, f, "An Astilba recovery code was used")

	if err := f.auth.RequestReset(context.Background(), "owner@example.test", auth.Source{Key: "test", Network: "test"}); err != nil {
		t.Fatal(err)
	}
	token := tokenFromLinkMail(t, f, "/reset")
	if _, err := f.auth.CompleteReset(context.Background(), token, "a reset password for this test", "test"); err != nil {
		t.Fatal(err)
	}
	awaitNotice(t, f, "Your Astilba password was reset")
}

// Every change takes from one budget with confirming identity, so a stolen session cannot guess
// at the account's code more often by choosing which change to attempt.
func TestCredentialGuessesShareTheConfirmationBudget(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	// Turning it on spent the first of the five attempts.
	mfaOwner(t, f, b)
	wrong := map[string]any{"current": map[string]string{"code": "000000"}}
	b.call("POST", "/api/v1/auth/mfa/disable", wrong, 400, nil)
	b.call("POST", "/api/v1/auth/mfa/recovery-codes", wrong, 400, nil)
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": "a guessed replacement", "current": map[string]string{"code": "000000"}}, 400, nil)
	b.reauthenticate(map[string]string{"code": "000000"}, 400)
	b.call("POST", "/api/v1/auth/mfa/disable", wrong, 429, nil)
	b.reauthenticate(map[string]string{"code": "000000"}, 429)
}

const securityNoticeKind = "platform_security_notice_v1"

func waitForSecurityNotice(t *testing.T, f *fixture, state string) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT state FROM river_job WHERE kind=$1 ORDER BY id DESC LIMIT 1", securityNoticeKind).Scan(&got); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if got == state {
			return
		}
	}
	t.Fatalf("the security notice stayed %q, want %q", got, state)
}

// A relay that refuses a notice is tried again, so the account holder still hears of the change.
func TestSecurityNoticeOutlastsAFailedSend(t *testing.T) {
	f := setup(t)
	relay := newResetRelay(t, f)
	relay.refuse.Store(true)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": "a second password here", "current": map[string]string{"password": f.password}}, 200, nil)
	waitForSecurityNotice(t, f, "retryable")

	relay.refuse.Store(false)
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE river_job SET state='available',scheduled_at=now() WHERE kind=$1 AND state='retryable'", securityNoticeKind); err != nil {
		t.Fatal(err)
	}
	if notice := receive(t, relay.messages); !strings.Contains(notice, "Subject: Your Astilba password was changed\n") {
		t.Fatalf("expected the password notice, got:\n%s", notice)
	}
	waitForSecurityNotice(t, f, "completed")
}

// An installation with no email has nobody to retry for.
func TestSecurityNoticeWithoutEmailIsCancelled(t *testing.T) {
	f := setup(t)
	unconfigured, err := mailer.New(mailer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.auth.Mail = unconfigured
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	b.call("POST", "/api/v1/auth/password", map[string]any{"password": "a second password here", "current": map[string]string{"password": f.password}}, 200, nil)
	waitForSecurityNotice(t, f, "cancelled")
	if logged := f.log.String(); !strings.Contains(logged, "account security notice not sent: email is not configured") {
		t.Fatalf("the cancelled notice was not logged: %q", logged)
	}
}
