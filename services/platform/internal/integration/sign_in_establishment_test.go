package integration

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// unsealableDevices refuses to seal device tokens while armed, as a token key that cannot be
// used would. Everything else goes to the real sealer.
type unsealableDevices struct {
	auth.TokenSealer
	armed atomic.Bool
}

func (u *unsealableDevices) Seal(kind secretbroker.TokenKind, plaintext []byte) ([]byte, error) {
	if kind == secretbroker.DeviceToken && u.armed.Load() {
		return nil, errors.New("device token key unavailable")
	}
	return u.TokenSealer.Seal(kind, plaintext)
}

// signedInSessions counts the stored sessions that would admit their holder to account: live,
// signed in to it, and on its current version.
func signedInSessions(t *testing.T, f *fixture, account uuid.UUID) int {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := f.st.Pool.QueryRow(ctx, "SELECT auth_version FROM accounts WHERE id=$1", account).Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.Pool.Query(ctx, "SELECT data FROM sessions WHERE expiry > clock_timestamp()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		_, values, err := f.apiServer.Sessions.Codec.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if values["account"] == account.String() && values["version"] == version {
			count++
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}

// browserCookies is every cookie b would send to path.
func browserCookies(t *testing.T, b *browser, path string) map[string]string {
	t.Helper()
	u, err := url.Parse(b.f.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, cookie := range b.client.Jar.Cookies(u) {
		out[cookie.Name] = cookie.Value
	}
	return out
}

// refuseSessionWrites makes every insert into the session store fail while it is armed, as a
// session store that cannot be written would, and returns its switch.
func refuseSessionWrites(t *testing.T, f *fixture) func(bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `CREATE FUNCTION refuse_session_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'session store unavailable'; END; $$`); err != nil {
		t.Fatal(err)
	}
	return func(armed bool) {
		t.Helper()
		statement := `DROP TRIGGER refuse_session_write ON sessions`
		if armed {
			statement = `CREATE TRIGGER refuse_session_write BEFORE INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION refuse_session_write()`
		}
		if _, err := f.st.Pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}

// A sign-in, reauthentication or credential change commits what it changes together with the
// session it stores, so one that fails before then changes nothing: a recovery or authenticator
// code stays unspent, a password or two-factor change is not applied, recovery codes are not
// replaced, an invitation is not used, and no security event or email is recorded. It signs
// nothing in and sends the browser neither a session cookie nor a device cookie, and the same
// request then succeeds. The failure is either the device token, the last step of establishing a
// session, or the session's write itself.
func TestFailedSignInChangesNothing(t *testing.T) {
	const email = "owner@example.test"
	password := func(f *fixture) map[string]string { return map[string]string{"password": f.password} }
	cases := []struct {
		name string
		// prepare signs in whatever the case needs and returns the browser that then fails, with
		// the request it fails on.
		prepare func(t *testing.T, f *fixture) (*browser, func() (int, []byte))
		// signedIn is how many sessions of the owner stay signed in after the failure, and stays
		// whether the failing browser's is one of them.
		signedIn int
		stays    bool
	}{
		{"sign in", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": f.password})
			}
		}, 0, false},
		{"two-factor sign-in", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			owner := f.browser(t)
			owner.login(email, f.password)
			secret := enrollSecret(t, owner)
			owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, time.Now()), "current": password(f)}, 200, nil)
			b := f.browser(t)
			if pending := b.login(email, f.password); !pending.MfaPending || pending.User != nil {
				t.Fatal("the sign-in did not wait for its second factor")
			}
			// The next window's code, since the confirmation spent this one's.
			code := codeAt(t, secret, time.Now().Add(30*time.Second))
			return b, func() (int, []byte) { return b.request("POST", "/api/v1/auth/mfa", map[string]string{"code": code}) }
		}, 1, false},
		{"two-factor sign-in with a recovery code", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			codes := enableMFA(t, f.browser(t).withLogin(email, f.password))
			b := f.browser(t)
			if pending := b.login(email, f.password); !pending.MfaPending {
				t.Fatal("the sign-in did not wait for its second factor")
			}
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/mfa", map[string]string{"code": codes.Codes[0]})
			}
		}, 1, false},
		{"reauthentication", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			return b, func() (int, []byte) { return b.request("POST", "/api/v1/auth/reauthenticate", password(f)) }
		}, 1, true},
		{"reauthentication with a recovery code", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			codes := enableMFA(t, b)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/reauthenticate", map[string]string{"code": codes.Codes[0]})
			}
		}, 1, true},
		{"password change", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": password(f)})
			}
		}, 1, true},
		{"turning on two-factor authentication", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			code := codeAt(t, enrollSecret(t, b), time.Now())
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": code, "current": password(f)})
			}
		}, 1, true},
		// The codes the account had stay valid: the same one confirms identity again after.
		{"replacing recovery codes", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			codes := enableMFA(t, b)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/mfa/recovery-codes", map[string]any{"current": map[string]string{"code": codes.Codes[0]}})
			}
		}, 1, true},
		{"turning off two-factor authentication", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t).withLogin(email, f.password)
			codes := enableMFA(t, b)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/mfa/disable", map[string]any{"current": map[string]string{"code": codes.Codes[0]}})
			}
		}, 1, true},
		{"accepting an invitation", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			owner := f.browser(t)
			ws := owner.login(email, f.password).Workspaces[0].Id
			owner.call("POST", "/api/v1/workspaces/"+ws.String()+"/invitations", map[string]string{"email": "invited@example.test", "role": "viewer"}, 201, nil)
			token := tokenFromLinkMail(t, f, "/invitation")
			b := f.browser(t)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token, "name": "Invited", "password": f.password})
			}
		}, 1, false},
		{"hosted sign-up", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			f.st.Hosted = true
			f.st.Hosting = hosting.Config{CohortLimit: 5, ServerLimit: 2}
			if err := f.auth.InviteHosted(context.Background(), "beta@example.test", f.apiServer.Origin); err != nil {
				t.Fatal(err)
			}
			token := tokenFromLinkMail(t, f, "/signup")
			b := f.browser(t)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/hosting/signup", map[string]string{"token": token, "workspace": "Beta", "name": "Beta", "password": f.password})
			}
		}, 0, false},
	}
	failures := []struct {
		name string
		arm  func(t *testing.T, f *fixture) func(bool)
	}{
		{"device token", func(_ *testing.T, f *fixture) func(bool) {
			tokens := &unsealableDevices{TokenSealer: f.auth.Tokens}
			f.auth.Tokens = tokens
			return tokens.armed.Store
		}},
		{"session write", refuseSessionWrites},
	}
	for _, failure := range failures {
		for _, c := range cases {
			t.Run(failure.name+"/"+c.name, func(t *testing.T) {
				f := setup(t)
				arm := failure.arm(t, f)
				b, request := c.prepare(t, f)
				before := browserCookies(t, b, "/api/v1/auth/login")
				credentials, events := accountCredentials(t, f), securityEvents(t, f)
				arm(true)
				status, body := request()
				arm(false)
				if status != http.StatusInternalServerError {
					t.Fatalf("status %d: %s", status, body)
				}
				if accountCredentials(t, f) != credentials {
					t.Fatal("the failed request changed the account's credentials")
				}
				if !slices.Equal(securityEvents(t, f), events) {
					t.Fatal("the failed request recorded a security event")
				}
				if got := signedInSessions(t, f, f.owner); got != c.signedIn {
					t.Fatalf("%d sessions signed in, want %d", got, c.signedIn)
				}
				if after := browserCookies(t, b, "/api/v1/auth/login"); len(after) != len(before) || after["astilba_session"] != before["astilba_session"] || after["astilba_device"] != before["astilba_device"] {
					t.Fatal("the failed request replaced the browser's session or device cookie")
				}
				if session := b.session(); (session.User != nil) != c.stays {
					t.Fatalf("the browser is signed in as %v", session.User)
				}
				// Whatever the request presented, a code, an enrolment or an invitation, is still unused.
				if status, body = request(); status != http.StatusOK {
					t.Fatalf("the same request then answered %d: %s", status, body)
				}
			})
		}
	}
}
