package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
)

const resetReason = "Lost their phone; identity confirmed on a call"

// operatorReset runs the operator's reset and checks the holder's notice: it says the operator
// did it, gives the reason, and sends the holder to this installation's reset page.
func operatorReset(t *testing.T, f *fixture) {
	t.Helper()
	account, err := f.auth.ResetMFA(context.Background(), " Owner@Example.test ", resetReason)
	if err != nil || account != f.owner {
		t.Fatalf("reset %s, %v", account, err)
	}
	select {
	case notice := <-f.notices:
		for _, want := range []string{"Subject: Two-factor authentication was reset", "To: owner@example.test", "The operator of this Astilba installation", "The reason they gave: " + resetReason, f.server.URL + "/reset,"} {
			if !strings.Contains(notice, want) {
				t.Fatalf("notice lacks %q:\n%s", want, notice)
			}
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the account holder was not told of the reset")
	}
	var reason string
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT reason FROM account_security_events WHERE account_id=$1 AND kind='mfa_reset'", f.owner).Scan(&reason); err != nil || reason != resetReason {
		t.Fatalf("the reset's record holds %q, %v", reason, err)
	}
}

// oldPasswordRefused checks that a password no longer signs in, and fails as any wrong password does.
func oldPasswordRefused(t *testing.T, f *fixture, password string) {
	t.Helper()
	status, body := f.browser(t).request("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.test", "password": password})
	if status != 400 || errorCode(t, body) != "invalid_credentials" {
		t.Fatalf("signing in with that password answered %d %s", status, body)
	}
}

// resetPassword takes the account back the way the notice says: through an emailed link.
func resetPassword(t *testing.T, f *fixture) {
	t.Helper()
	clearBudgets(t, f)
	if err := f.auth.RequestReset(context.Background(), "owner@example.test", auth.Source{Key: "test", Network: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.CompleteReset(context.Background(), tokenFromLinkMail(t, f, "/reset"), "the holder's new password", "test"); err != nil {
		t.Fatal(err)
	}
	awaitNotice(t, f, "Your Astilba password was reset")
	if session := f.browser(t).login("owner@example.test", "the holder's new password"); session.User == nil || session.MfaPending {
		t.Fatal("the holder cannot sign in with their new password alone")
	}
}

// An account holder who lost their authenticator, with no recovery codes left, gets back in
// after the operator's reset and an emailed password reset. Everything signed in before ends,
// and the old password no longer signs in.
func TestOperatorResetRecoversALostAuthenticator(t *testing.T) {
	f := setup(t)
	b, _, _, path := automationEnvironment(t, f)
	mfaOwner(t, f, b)
	token := *issueAutomation(t, b, path, domain.EnvironmentRead).Token
	bearerCall(t, f, token, "GET", "/api/v1/automation/context", nil, 200, nil)
	if pending := f.browser(t).login("owner@example.test", f.password); !pending.MfaPending {
		t.Fatal("expected the account to need its authenticator")
	}

	operatorReset(t, f)
	if b.session().User != nil {
		t.Fatal("the holder's session outlived the reset")
	}
	bearerCall(t, f, token, "GET", "/api/v1/automation/context", nil, 401, nil)
	if got := securityEvents(t, f); strings.Join(got, ",") != "mfa_enabled,mfa_reset" {
		t.Fatal("events: ", got)
	}
	var codes int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM recovery_codes WHERE account_id=$1", f.owner).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("%d recovery codes remain, %v", codes, err)
	}
	oldPasswordRefused(t, f, f.password)
	resetPassword(t, f)
}

// Someone who knew the password and enrolled their own authenticator loses the account at the
// operator's reset: their session ends, the password they knew no longer signs in, and a reset
// link they took while they held the mailbox no longer works. Without these, they could sign in
// with no second factor the moment the authenticator went.
func TestOperatorResetRemovesAForeignAuthenticator(t *testing.T) {
	f := setup(t)
	intruder := f.browser(t)
	intruder.login("owner@example.test", f.password)
	mfaOwner(t, f, intruder)
	if pending := f.browser(t).login("owner@example.test", f.password); !pending.MfaPending {
		t.Fatal("expected the holder to need the intruder's authenticator")
	}
	if err := f.auth.RequestReset(context.Background(), "owner@example.test", auth.Source{Key: "intruder", Network: "intruder"}); err != nil {
		t.Fatal(err)
	}
	link := tokenFromLinkMail(t, f, "/reset")

	operatorReset(t, f)
	if intruder.session().User != nil {
		t.Fatal("the intruder's session outlived the reset")
	}
	oldPasswordRefused(t, f, f.password)
	if _, err := f.auth.CompleteReset(context.Background(), link, "the intruder's new password", "intruder"); !errors.Is(err, domain.Credentials) {
		t.Fatal("a reset link from before the reset still works: ", err)
	}
	// The intruder's authenticator is gone too, so a code from it confirms nothing.
	var stillEnrolled bool
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT totp_secret IS NOT NULL OR totp_pending IS NOT NULL FROM accounts WHERE id=$1", f.owner).Scan(&stillEnrolled); err != nil || stillEnrolled {
		t.Fatalf("the intruder's authenticator outlived the reset (%v)", err)
	}
	resetPassword(t, f)
}

// The reset refuses an installation without email, where the holder could not reset their
// password afterwards; an address with no account; an account without two-factor
// authentication; and a reason that is missing, too long for one line of mail, or could change
// what the notice says. It changes nothing then.
func TestOperatorResetRefusals(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	before := signInCredentials(t, f)
	unconfigured, err := mailer.New(mailer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	withoutMail := *f.auth
	withoutMail.Mail = unconfigured
	if _, err := withoutMail.ResetMFA(ctx, "owner@example.test", resetReason); !errors.Is(err, auth.ErrMailUnconfigured) {
		t.Fatal("an installation without email: ", err)
	}
	if _, err := f.auth.ResetMFA(ctx, "nobody@example.test", resetReason); !errors.Is(err, domain.NotFound) {
		t.Fatal("an unknown address: ", err)
	}
	// The longest reason passes its checks, so the account is what is refused.
	for _, reason := range []string{resetReason, strings.Repeat("x", 500), strings.Repeat("é", 250)} {
		if _, err := f.auth.ResetMFA(ctx, "owner@example.test", reason); !errors.Is(err, domain.Conflict) {
			t.Fatal("an account without two-factor authentication: ", err)
		}
	}
	overrides := string(rune(0x202e)) + "reversed"
	separated := "one" + string(rune(0x2028)) + "two"
	paragraphs := "one" + string(rune(0x2029)) + "two"
	for _, reason := range []string{"", "   ", "two\nlines", "a tab\there", overrides, separated, paragraphs, strings.Repeat("x", 501), strings.Repeat("é", 251)} {
		var invalid *domain.Error
		if _, err := f.auth.ResetMFA(ctx, "owner@example.test", reason); !errors.As(err, &invalid) || invalid.Fields[0] != "reason" {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	if _, err := f.auth.ResetMFA(ctx, "not an address", resetReason); err == nil {
		t.Fatal("a malformed address was accepted")
	}
	if after := signInCredentials(t, f); after != before || len(securityEvents(t, f)) != 0 {
		t.Fatal("a refused reset changed the account")
	}
}
