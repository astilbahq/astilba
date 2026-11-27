package integration

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
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

// A sign-in, reauthentication or credential change that fails after renewing the session's
// token leaves nothing it began signed in, and sends the browser neither a session cookie nor a
// device cookie. Here the failure is the device token, the last step of establishing a session.
func TestFailedEstablishmentSignsNothingIn(t *testing.T) {
	const email = "owner@example.test"
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
			owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, time.Now()), "current": map[string]string{"password": f.password}}, 200, nil)
			b := f.browser(t)
			if pending := b.login(email, f.password); !pending.MfaPending || pending.User != nil {
				t.Fatal("the sign-in did not wait for its second factor")
			}
			return b, func() (int, []byte) {
				// The next window's code, since the confirmation spent this one's. A recovery code
				// would end the owner's session by changing the account's version.
				return b.request("POST", "/api/v1/auth/mfa", map[string]string{"code": codeAt(t, secret, time.Now().Add(30*time.Second))})
			}
		}, 1, false},
		{"credential change", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t)
			b.login(email, f.password)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": f.password}})
			}
		}, 0, false},
		{"reauthentication", func(t *testing.T, f *fixture) (*browser, func() (int, []byte)) {
			b := f.browser(t)
			b.login(email, f.password)
			return b, func() (int, []byte) {
				return b.request("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password})
			}
		}, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := setup(t)
			tokens := &unsealableDevices{TokenSealer: f.auth.Tokens}
			f.auth.Tokens = tokens
			b, fail := c.prepare(t, f)
			before := browserCookies(t, b, "/api/v1/auth/login")
			tokens.armed.Store(true)
			status, body := fail()
			tokens.armed.Store(false)
			if status != http.StatusInternalServerError {
				t.Fatalf("status %d: %s", status, body)
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
		})
	}
}
