package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/pquerna/otp/totp"
)

// An authenticator enrolment belongs to the browser session that began it. Another session of
// the same account, such as an old one someone else holds, can neither replace the secret its
// holder scanned nor confirm it, and the enrolment still works after its session renews.
func TestMFAEnrolmentBelongsToTheSessionThatBeganIt(t *testing.T) {
	f := setup(t)
	owner, other := f.browser(t), f.browser(t)
	owner.login("owner@example.test", f.password)
	other.login("owner@example.test", f.password)
	secret := enrollSecret(t, owner)
	code := func(secret string) string {
		t.Helper()
		value, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	var refused api.Error
	other.call("POST", "/api/v1/auth/mfa/enroll", nil, 409, &refused)
	if refused.Code != "mfa_setup_elsewhere" {
		t.Fatalf("another session's enrolment refused as %q", refused.Code)
	}
	confirm := map[string]any{"code": code(secret), "current": map[string]string{"password": f.password}}
	other.call("POST", "/api/v1/auth/mfa/confirm", confirm, 400, nil)
	if v := other.session(); v.User == nil || v.User.MfaEnabled {
		t.Fatal("another session confirmed the enrolment")
	}

	// Confirming credentials renews the token, within the same sign-in.
	var renewed api.Session
	owner.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, &renewed)
	owner.csrf = renewed.CsrfToken
	var codes api.RecoveryCodes
	owner.call("POST", "/api/v1/auth/mfa/confirm", confirm, 200, &codes)
	if !codes.Session.User.MfaEnabled {
		t.Fatal("the session that began the enrolment could not confirm it")
	}
}

// An enrolment gives way once it is no one's: its session signed out, or it lapsed. A session
// may start its own again, which replaces the secret it was shown before.
func TestMFAEnrolmentGivesWayWhenItsSessionEnds(t *testing.T) {
	ctx := context.Background()
	for _, ending := range []string{"logout", "lapse", "restart"} {
		t.Run(ending, func(t *testing.T) {
			f := setup(t)
			first, second := f.browser(t), f.browser(t)
			first.login("owner@example.test", f.password)
			second.login("owner@example.test", f.password)
			enrollSecret(t, first)
			next := second
			switch ending {
			case "logout":
				first.call("POST", "/api/v1/auth/logout", nil, 204, nil)
			case "lapse":
				if _, err := f.st.Pool.Exec(ctx, "UPDATE accounts SET totp_pending_expiry=clock_timestamp()-interval '1 hour' WHERE id=$1", f.owner); err != nil {
					t.Fatal(err)
				}
			case "restart":
				next = first
			}
			secret := enrollSecret(t, next)
			value, err := totp.GenerateCode(secret, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var codes api.RecoveryCodes
			next.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": value, "current": map[string]string{"password": f.password}}, 200, &codes)
			if !codes.Session.User.MfaEnabled {
				t.Fatal("the newer enrolment did not turn on two-factor authentication")
			}
		})
	}
}
