package integration

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/pquerna/otp/totp"
)

type heldResponse struct {
	status  int
	cookies []*http.Cookie
	body    []byte
}

func holdSessions(t *testing.T, f *fixture) (*heldSessionStore, func()) {
	t.Helper()
	gate := &heldSessionStore{CtxStore: f.apiServer.Sessions.Store.(scs.CtxStore), loaded: make(chan struct{}), release: make(chan struct{})}
	f.apiServer.Sessions.Store = gate
	release := sync.OnceFunc(func() { close(gate.release) })
	t.Cleanup(release)
	return gate, release
}

// sendHeld sends a request with these cookies, returning once an armed store is
// holding it. The returned function waits for the response after the release.
func sendHeld(t *testing.T, b *browser, gate *heldSessionStore, cookies []*http.Cookie, path string, body any) func() heldResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var raw []byte
	if body != nil {
		var err error
		if raw, err = stablejson.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, "POST", b.f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", b.f.server.URL)
	request.Header.Set("X-CSRF-Token", b.csrf)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	finished := make(chan heldResponse, 1)
	failed := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			failed <- err
			return
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			failed <- err
			return
		}
		finished <- heldResponse{response.StatusCode, response.Cookies(), data}
	}()
	select {
	case <-gate.loaded:
	case err := <-failed:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("request did not reach the held session store")
	}
	return func() heldResponse {
		t.Helper()
		select {
		case result := <-finished:
			return result
		case err := <-failed:
			t.Fatal(err)
		}
		return heldResponse{}
	}
}

func sessionFromCookies(t *testing.T, b *browser, cookies []*http.Cookie) api.Session {
	t.Helper()
	request, err := http.NewRequest("GET", b.f.server.URL+"/api/v1/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var session api.Session
	if err := json.UnmarshalDecode(jsontext.NewDecoder(response.Body), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func enrollSecret(t *testing.T, b *browser) string {
	t.Helper()
	var enrollment api.MFAEnrollment
	b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	return enrollment.Secret
}

func enableMFA(t *testing.T, b *browser) api.RecoveryCodes {
	t.Helper()
	code, err := totp.GenerateCode(enrollSecret(t, b), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codes api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
	b.csrf = codes.Session.CsrfToken
	return codes
}

// The account change passes its version check because logout does not bump it,
// so only the session save can stop it recreating what logout deleted.
func TestAccountChangeDoesNotRecreateLoggedOutSession(t *testing.T) {
	for _, change := range []struct {
		name, path string
		body       func(t *testing.T, b *browser) any
	}{
		{"change password", "/api/v1/auth/password", func(*testing.T, *browser) any {
			return map[string]string{"password": "replacement password here"}
		}},
		{"confirm MFA", "/api/v1/auth/mfa/confirm", func(t *testing.T, b *browser) any {
			code, err := totp.GenerateCode(enrollSecret(t, b), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return map[string]string{"code": code}
		}},
		{"regenerate recovery codes", "/api/v1/auth/mfa/recovery-codes", func(t *testing.T, b *browser) any {
			enableMFA(t, b)
			return nil
		}},
		{"disable MFA", "/api/v1/auth/mfa/disable", func(t *testing.T, b *browser) any {
			enableMFA(t, b)
			return nil
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := setup(t)
			gate, release := holdSessions(t, f)
			b := f.browser(t)
			b.login("owner@example.test", f.password)
			body := change.body(t, b)

			gate.armed.Store(true)
			finish := sendHeld(t, b, gate, b.client.Jar.Cookies(mustURL(f.server.URL)), change.path, body)
			b.call("POST", "/api/v1/auth/logout", nil, 204, nil)
			release()
			result := finish()

			if result.status != 401 || len(result.cookies) != 0 {
				t.Fatalf("status %d, cookies %d: %s", result.status, len(result.cookies), result.body)
			}
			var failure api.Error
			if err := json.Unmarshal(result.body, &failure); err != nil || failure.Code != "unauthenticated" {
				t.Fatalf("unexpected body %s: %v", result.body, err)
			}
			var rows int
			if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions").Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("%d session rows after logout: %v", rows, err)
			}
		})
	}
}

// Signing in proves a credential, so it owes nothing to whichever session the
// browser happened to present.
func TestSignInSucceedsWithoutThePresentedSession(t *testing.T) {
	for _, step := range []struct {
		name, path string
		prepare    func(t *testing.T, f *fixture, b *browser) any
	}{
		{"login", "/api/v1/auth/login", func(_ *testing.T, f *fixture, _ *browser) any {
			return map[string]string{"email": "owner@example.test", "password": f.password}
		}},
		{"verify MFA", "/api/v1/auth/mfa", func(t *testing.T, f *fixture, b *browser) any {
			owner := f.browser(t)
			owner.login("owner@example.test", f.password)
			codes := enableMFA(t, owner)
			if pending := b.login("owner@example.test", f.password); !pending.MfaPending {
				t.Fatal("expected a pending MFA login")
			}
			return map[string]string{"code": codes.Codes[0]}
		}},
	} {
		t.Run(step.name, func(t *testing.T) {
			f := setup(t)
			gate, release := holdSessions(t, f)
			b := f.browser(t)
			body := step.prepare(t, f, b)

			gate.armed.Store(true)
			finish := sendHeld(t, b, gate, b.client.Jar.Cookies(mustURL(f.server.URL)), step.path, body)
			if _, err := f.st.Pool.Exec(context.Background(), "DELETE FROM sessions"); err != nil {
				t.Fatal(err)
			}
			release()
			result := finish()

			if result.status != 200 || len(result.cookies) != 1 {
				t.Fatalf("status %d, cookies %d: %s", result.status, len(result.cookies), result.body)
			}
			if session := sessionFromCookies(t, b, result.cookies); session.User == nil || session.User.Id != f.owner {
				t.Fatal("the new cookie did not authenticate the owner")
			}
		})
	}
}

// Once the change commits, any request still carrying the old cookie finds it stale.
// That cleanup must not read as a logout and fail the change that caused it.
func TestAccountChangeSurvivesStaleRequestWithOldCookie(t *testing.T) {
	f := setup(t)
	gate, release := holdSessions(t, f)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	old := b.client.Jar.Cookies(mustURL(f.server.URL))

	gate.armedCommit.Store(true)
	finish := sendHeld(t, b, gate, old, "/api/v1/auth/password", map[string]string{"password": "replacement password here"})
	if stale := sessionFromCookies(t, b, old); stale.User != nil {
		t.Fatal("the old cookie survived the password change")
	}
	release()
	result := finish()

	if result.status != 200 || len(result.cookies) != 1 {
		t.Fatalf("status %d, cookies %d: %s", result.status, len(result.cookies), result.body)
	}
	if session := sessionFromCookies(t, b, result.cookies); session.User == nil || session.User.Id != f.owner {
		t.Fatal("the renewed cookie did not authenticate the owner")
	}
}
