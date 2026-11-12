package integration

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/pquerna/otp/totp"
)

// Using a recovery code ends the account's other sessions and their terminals, and the
// browser that used it stays signed in.
func TestTroubleshootingEndsWhenARecoveryCodeIsUsed(t *testing.T) {
	x := relayFixture(t)
	var enrollment api.MFAEnrollment
	x.owner.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var recovery api.RecoveryCodes
	x.owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &recovery)
	x.owner.csrf = recovery.Session.CsrfToken

	opened, _, _ := x.connected(t, x.owner, 1)
	elsewhere := x.f.browser(t)
	elsewhere.login("owner@example.test", x.f.password)
	var verified api.Session
	elsewhere.call("POST", "/api/v1/auth/mfa", map[string]string{"code": recovery.Codes[0]}, 200, &verified)
	x.waitClosed(t, opened.Session.Id, "account security changed")
	if elsewhere.session().User == nil {
		t.Fatal("the browser that used the code was signed out")
	}
	if x.owner.session().User != nil {
		t.Fatal("the other browser stayed signed in")
	}

	// Confirming credentials with a code ends a terminal in the same browser too.
	elsewhere.csrf = verified.CsrfToken
	opened, _, _ = x.connected(t, elsewhere, 1)
	var renewed api.Session
	elsewhere.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"code": recovery.Codes[1]}, 200, &renewed)
	x.waitClosed(t, opened.Session.Id, "account security changed")
	if elsewhere.session().User == nil {
		t.Fatal("confirming credentials with a code signed the browser out")
	}
}
