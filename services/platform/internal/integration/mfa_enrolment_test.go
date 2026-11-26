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
	// Another session's attempts, more than the confirmation limit allows and with the right
	// password or a wrong one, spend none of the owner's budgets.
	budgets := func() string {
		t.Helper()
		var state string
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT coalesce(string_agg(encode(scope_hash,'hex') || ':' || attempts, ',' ORDER BY scope_hash), '') FROM auth_attempts").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := budgets()
	confirm := map[string]any{"code": code(secret), "current": map[string]string{"password": f.password}}
	for attempt := range 6 {
		body := confirm
		if attempt%2 == 1 {
			body = map[string]any{"code": code(secret), "current": map[string]string{"password": "not the password at all"}}
		}
		var failure api.Error
		other.call("POST", "/api/v1/auth/mfa/confirm", body, 400, &failure)
		if failure.Code != "invalid_input" {
			t.Fatalf("another session's confirmation refused as %q", failure.Code)
		}
	}
	if after := budgets(); after != before {
		t.Fatalf("another session's confirmations spent the owner's budgets:\n%s\n%s", before, after)
	}
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
	// A logout with no enrolment to end does not write to accounts, so a hold on the whole
	// table, as a custody conversion takes, does not keep it waiting either.
	third := f.browser(t)
	third.login("owner@example.test", f.password)
	held, err = f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback(ctx) })
	if _, err = held.Exec(ctx, "LOCK TABLE accounts IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	logout = third.send("POST", "/api/v1/auth/logout", nil)
	select {
	case reply := <-logout:
		requireReply(t, reply, 204)
	case <-time.After(5 * time.Second):
		t.Fatal("logout waited for a hold on accounts")
	}
}

// A two-factor sign-in continues its pending sign-in's lineage only while that lasts. If a logout of
// the pending sign-in lands while the code is checked, the sign-in still succeeds, since it proves
// a credential, but in a lineage of its own: one that has signed out is never signed into again,
// so it cannot hold an enrolment off.
func TestMFASignInAfterItsLogoutStaysOutOfTheEndedLineage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enrolling := f.browser(t)
	enrolling.login("owner@example.test", f.password)
	codes := enableMFA(t, enrolling)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	lineages := func() map[string]bool {
		t.Helper()
		rows, err := f.st.Pool.Query(ctx, "SELECT DISTINCT lineage::text FROM sessions")
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var lineage string
			if err = rows.Scan(&lineage); err != nil {
				t.Fatal(err)
			}
			found[lineage] = true
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return found
	}
	before := lineages()
	held, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback(ctx) })
	if _, err = held.Exec(ctx, "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", f.owner); err != nil {
		t.Fatal(err)
	}
	verify := b.send("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[0]})
	waitForLock(t, f, "-- name: LockAccount")
	requireReply(t, <-b.send("POST", "/api/v1/auth/logout", nil), 204)
	afterLogout := lineages()
	if err = held.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	requireReply(t, <-verify, 200)
	ended := 0
	for lineage := range before {
		if afterLogout[lineage] {
			continue
		}
		ended++
		if lineages()[lineage] {
			t.Fatal("the sign-in signed into the lineage its logout ended")
		}
	}
	if ended != 1 {
		t.Fatalf("the logout ended %d lineages, want 1", ended)
	}
}
