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

// An enrolment gives way once it is no one's. Ending its session, by logout or by a password
// change that ends every session, frees it at once; one that lapsed gives way to the next. A
// session may start its own again, which replaces the secret it was shown before.
func TestMFAEnrolmentGivesWayWhenItsSessionEnds(t *testing.T) {
	ctx := context.Background()
	for _, ending := range []string{"logout", "password", "lapse", "restart"} {
		t.Run(ending, func(t *testing.T) {
			f := setup(t)
			first, second := f.browser(t), f.browser(t)
			first.login("owner@example.test", f.password)
			second.login("owner@example.test", f.password)
			enrollSecret(t, first)
			next, password := second, f.password
			switch ending {
			case "logout":
				first.call("POST", "/api/v1/auth/logout", nil, 204, nil)
			case "password":
				password = "another correct horse battery staple"
				var changed api.Session
				second.call("POST", "/api/v1/auth/password", map[string]any{"current": map[string]string{"password": f.password}, "password": password}, 200, &changed)
				second.csrf = changed.CsrfToken
			case "lapse":
				if _, err := f.st.Pool.Exec(ctx, "UPDATE accounts SET totp_pending_expiry=clock_timestamp()-interval '1 hour' WHERE id=$1", f.owner); err != nil {
					t.Fatal(err)
				}
			case "restart":
				next = first
			}
			if ending == "logout" || ending == "password" {
				var held bool
				if err := f.st.Pool.QueryRow(ctx, "SELECT totp_pending IS NOT NULL OR totp_pending_expiry IS NOT NULL OR totp_pending_lineage IS NOT NULL FROM accounts WHERE id=$1", f.owner).Scan(&held); err != nil || held {
					t.Fatal("ending the session left its enrolment pending", err)
				}
			}
			secret := enrollSecret(t, next)
			value, err := totp.GenerateCode(secret, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var codes api.RecoveryCodes
			next.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": value, "current": map[string]string{"password": password}}, 200, &codes)
			if !codes.Session.User.MfaEnabled {
				t.Fatal("the newer enrolment did not turn on two-factor authentication")
			}
		})
	}
}

// Freeing an enrolment never makes a logout wait for the account's lock, which a credential
// change or an import may hold while it rechecks the session; the enrolment still gives way.
func TestMFAEnrolmentLogoutDoesNotWaitForTheAccount(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	first, second := f.browser(t), f.browser(t)
	first.login("owner@example.test", f.password)
	second.login("owner@example.test", f.password)
	enrollSecret(t, first)
	held, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback(ctx) })
	if _, err = held.Exec(ctx, "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", f.owner); err != nil {
		t.Fatal(err)
	}
	logout := first.send("POST", "/api/v1/auth/logout", nil)
	select {
	case reply := <-logout:
		requireReply(t, reply, 204)
	case <-time.After(5 * time.Second):
		t.Fatal("logout waited for the account's lock")
	}
	if err = held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	enrollSecret(t, second)
}
