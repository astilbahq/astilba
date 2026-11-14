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

// Each of these changes takes a current credential, as confirming identity does, so it
// restarts recent authentication. Unlike signing in, it keeps the session's absolute
// lifetime, so repeating changes cannot carry the session past it.
func TestAccountChangesRotateWithinTheSessionLifetime(t *testing.T) {
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

	var recovery []string
	var secret string
	changes := []struct {
		name   string
		change func(now time.Time)
	}{
		{"change password", func(time.Time) {
			var session api.Session
			owner.call("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": f.password}}, 200, &session)
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
			owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": code, "current": map[string]string{"password": "replacement password here"}}, 200, &codes)
			owner.csrf = codes.Session.CsrfToken
			recovery, secret = codes.Codes, enrollment.Secret
		}},
		{"regenerate recovery codes", func(time.Time) {
			var codes api.RecoveryCodes
			owner.call("POST", "/api/v1/auth/mfa/recovery-codes", map[string]any{"current": map[string]string{"code": recovery[0]}}, 200, &codes)
			owner.csrf = codes.Session.CsrfToken
			recovery = codes.Codes
		}},
		{"disable MFA", func(now time.Time) {
			var session api.Session
			owner.call("POST", "/api/v1/auth/mfa/disable", map[string]any{"current": map[string]string{"code": codeAt(t, secret, now)}}, 200, &session)
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

	// The last change ran four minutes after signing in, and restarted the window.
	at(15*time.Minute + time.Second)
	if !owner.session().RecentAuth {
		t.Fatal("an account change did not restart recent authentication")
	}
	at(19*time.Minute + time.Second)
	if owner.session().RecentAuth {
		t.Fatal("recent authentication outlasted its window")
	}

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
