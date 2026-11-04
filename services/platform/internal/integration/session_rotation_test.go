package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
)

// Each of these changes needs only a recent session, so if one refreshed the
// session's limits, repeating it would keep recent authentication true forever
// and carry the session past its absolute lifetime.
func TestAccountChangesRotateWithoutRenewingSessionLimits(t *testing.T) {
	f := setup(t)
	signedIn := time.Now()
	at := func(elapsed time.Duration) time.Time {
		now := signedIn.Add(elapsed)
		f.auth.Now = func() time.Time { return now }
		return now
	}
	at(0)
	owner := f.browser(t)
	owner.login("owner@example.test", f.password)
	token, deadline := storedSession(t, f, owner)

	changes := []struct {
		name   string
		change func(now time.Time)
	}{
		{"change password", func(time.Time) {
			var session api.Session
			owner.call("POST", "/api/v1/auth/password", map[string]string{"password": "replacement password here"}, 200, &session)
			owner.csrf = session.CsrfToken
		}},
		{"confirm MFA", func(now time.Time) {
			var enrollment api.MFAEnrollment
			owner.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
			code, err := totp.GenerateCode(enrollment.Secret, now)
			if err != nil {
				t.Fatal(err)
			}
			var codes api.RecoveryCodes
			owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
			owner.csrf = codes.Session.CsrfToken
		}},
		{"regenerate recovery codes", func(time.Time) {
			var codes api.RecoveryCodes
			owner.call("POST", "/api/v1/auth/mfa/recovery-codes", nil, 200, &codes)
			owner.csrf = codes.Session.CsrfToken
		}},
		{"disable MFA", func(time.Time) {
			var session api.Session
			owner.call("POST", "/api/v1/auth/mfa/disable", nil, 200, &session)
			owner.csrf = session.CsrfToken
		}},
	}
	for i, c := range changes {
		csrf := owner.csrf
		c.change(at(time.Duration(i+1) * time.Minute))
		rotated, kept := storedSession(t, f, owner)
		if rotated == token || owner.csrf == csrf {
			t.Fatalf("%s kept the session or CSRF token", c.name)
		}
		if !kept.Equal(deadline) {
			t.Fatalf("%s moved the absolute deadline from %v to %v", c.name, deadline, kept)
		}
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT 1 FROM sessions WHERE token=$1", hashedSessionToken(token)).Scan(new(int)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s left the previous token usable: %v", c.name, err)
		}
		token = rotated
	}

	// Every change above ran within five minutes of this instant, so any refresh
	// would still count as recent here.
	at(5*time.Minute + time.Second)
	if owner.session().RecentAuth {
		t.Fatal("an account change refreshed recent authentication")
	}
	owner.call("POST", "/api/v1/auth/mfa/enroll", nil, 403, nil)

	var confirmed api.Session
	owner.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": "replacement password here"}, 200, &confirmed)
	owner.csrf = confirmed.CsrfToken
	if !confirmed.RecentAuth {
		t.Fatal("reauthentication did not refresh recent authentication")
	}
	if _, renewed := storedSession(t, f, owner); !renewed.After(deadline) {
		t.Fatal("reauthentication did not restart the absolute lifetime")
	}
}

func storedSession(t *testing.T, f *fixture, b *browser) (string, time.Time) {
	t.Helper()
	u, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cookies := b.client.Jar.Cookies(u)
	if len(cookies) != 1 {
		t.Fatal("expected one browser session cookie")
	}
	var data []byte
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT data FROM sessions WHERE token=$1", hashedSessionToken(cookies[0].Value)).Scan(&data); err != nil {
		t.Fatal(err)
	}
	deadline, _, err := f.apiServer.Sessions.Codec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return cookies[0].Value, deadline
}

func hashedSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
