package integration

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/httpapi"
	"github.com/google/uuid"
)

type heldSessionStore struct {
	scs.CtxStore
	armed   atomic.Bool
	loaded  chan struct{}
	release chan struct{}
	// armedCommit holds the next save instead, after its handler has run.
	armedCommit atomic.Bool
}

func (s *heldSessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	data, found, err := s.CtxStore.FindCtx(ctx, token)
	if err == nil && found && s.armed.CompareAndSwap(true, false) {
		close(s.loaded)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return data, found, err
}

func (s *heldSessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	if s.armedCommit.CompareAndSwap(true, false) {
		close(s.loaded)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.CtxStore.CommitCtx(ctx, token, data, expiry)
}

func TestBrowserSessionRevocationSurvivesDelayedResponse(t *testing.T) {
	for _, action := range []string{"logout", "renewal", "logout_stream"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			gate := &heldSessionStore{CtxStore: f.apiServer.Sessions.Store.(scs.CtxStore), loaded: make(chan struct{}), release: make(chan struct{})}
			f.apiServer.Sessions.Store = gate
			release := sync.OnceFunc(func() { close(gate.release) })
			defer release()
			b := f.browser(t)
			workspace := b.login("owner@example.test", f.password).Workspaces[0].Id
			path := "/api/v1/session"
			if action == "logout_stream" {
				var project api.Project
				base := "/api/v1/workspaces/" + workspace.String() + "/projects"
				b.call("POST", base, map[string]string{"name": "Events"}, 201, &project)
				var environment api.Environment
				base += "/" + project.Id.String() + "/environments"
				b.call("POST", base, map[string]string{"name": "Test"}, 201, &environment)
				path = base + "/" + environment.Id.String() + "/events"
			}
			u, _ := url.Parse(f.server.URL)
			cookies := b.client.Jar.Cookies(u)
			if len(cookies) != 1 {
				t.Fatal("expected one browser session cookie")
			}

			// A second management process revokes the row while the first has
			// already fetched its authenticated data from real PostgreSQL.
			replica, cleanup := httpapi.New(f.st, f.auth, f.server.URL, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
			t.Cleanup(cleanup)
			replica.Execution = f.execution
			handler, err := replica.Handler(http.NotFoundHandler())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			replica.Origin = server.URL
			t.Cleanup(server.Close)
			otherFixture := *f
			otherFixture.server, otherFixture.apiServer = server, replica
			other := &browser{t: t, f: &otherFixture, client: b.client, csrf: b.csrf}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+path, nil)
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
			type outcome struct {
				response *http.Response
				body     []byte
				err      error
			}
			finished := make(chan outcome, 1)
			gate.armed.Store(true)
			go func() {
				response, err := http.DefaultClient.Do(request)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(response.Body)
					response.Body.Close()
				}
				finished <- outcome{response, body, err}
			}()
			select {
			case <-gate.loaded:
			case <-ctx.Done():
				t.Fatal("session request did not reach the held store read")
			}
			if action != "renewal" {
				other.call("POST", "/api/v1/auth/logout", nil, 204, nil)
			} else {
				var renewed api.Session
				other.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, &renewed)
				other.csrf = renewed.CsrfToken
				if renewed.User == nil || renewed.User.Id != f.owner {
					t.Fatal("renewal did not preserve the authenticated owner")
				}
			}
			release()
			result := <-finished
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.response.StatusCode != 401 || len(result.response.Cookies()) != 0 {
				t.Fatalf("stale response: status %d, cookies %d", result.response.StatusCode, len(result.response.Cookies()))
			}
			decoder := jsontext.NewDecoder(bytes.NewReader(result.body))
			var failure api.Error
			if err := json.UnmarshalDecode(decoder, &failure); err != nil || failure.Code != "unauthenticated" {
				t.Fatal("stale response did not contain the authentication error", err)
			}
			if err := json.UnmarshalDecode(decoder, new(any)); err != io.EOF {
				t.Fatal("stale handler payload followed the authentication error", err)
			}
			old, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/workspaces/"+workspace.String()+"/projects", nil)
			for _, cookie := range cookies {
				old.AddCookie(cookie)
			}
			response, err := http.DefaultClient.Do(old)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 401 {
				t.Fatalf("revoked cookie workspace access returned %d", response.StatusCode)
			}
			if action == "renewal" {
				var shortened time.Time
				if err := f.st.Pool.QueryRow(ctx, "UPDATE sessions SET expiry=now()+interval '1 minute' RETURNING expiry").Scan(&shortened); err != nil {
					t.Fatal(err)
				}
				if v := other.session(); v.User == nil || v.User.Id != f.owner {
					t.Fatal("stale response invalidated the renewed session")
				}
				var expiry time.Time
				if err := f.st.Pool.QueryRow(ctx, "SELECT expiry FROM sessions").Scan(&expiry); err != nil || !expiry.After(shortened.Add(10*time.Minute)) {
					t.Fatal("active session idle expiry did not refresh", err)
				}
			}
		})
	}
}

// A browser applies Set-Cookie in arrival order, so any response to a request carrying
// an old token can land after the response that renewed it.
func TestStaleRequestsLeaveRenewedCookieAlone(t *testing.T) {
	f := setup(t)
	target := mustURL(f.server.URL)
	send := func(method, path, csrf string, cookie *http.Cookie, body string) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", f.server.URL)
		request.Header.Set("X-CSRF-Token", csrf)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request.AddCookie(cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, data
	}
	signedIn := func() (*browser, *http.Cookie, uuid.UUID) {
		t.Helper()
		b := f.browser(t)
		workspace := b.login("owner@example.test", f.password).Workspaces[0].Id
		return b, b.client.Jar.Cookies(target)[0], workspace
	}

	// The cookie must outlast the idle window, since activity no longer rewrites it.
	guest := f.browser(t)
	response, data := send("POST", "/api/v1/auth/login", guest.csrf, guest.client.Jar.Cookies(target)[0], `{"email":"owner@example.test","password":"`+f.password+`"}`)
	if response.StatusCode != 200 || len(response.Cookies()) != 1 || response.Cookies()[0].MaxAge < int((11*time.Hour).Seconds()) {
		t.Fatalf("login: status %d, cookies %v: %s", response.StatusCode, response.Cookies(), data)
	}
	guest.client.Jar.SetCookies(target, response.Cookies())
	var signIn api.Session
	if err := json.Unmarshal(data, &signIn); err != nil {
		t.Fatal(err)
	}
	guest.csrf = signIn.CsrfToken
	guestCookie := guest.client.Jar.Cookies(target)[0]

	// An idle refresh that commits just before the old row's deletion.
	if response, data = send("GET", "/api/v1/session", "", guestCookie, ""); response.StatusCode != 200 || len(response.Cookies()) != 0 {
		t.Fatalf("refresh: status %d, cookies %v: %s", response.StatusCode, response.Cookies(), data)
	}

	reader, readerCookie, workspace := signedIn()
	writer, writerCookie, _ := signedIn()
	changer, _, _ := signedIn()
	var renewed api.Session
	replacement := "replacement password here"
	changer.call("POST", "/api/v1/auth/password", map[string]string{"password": replacement}, 200, &renewed)
	changer.csrf = renewed.CsrfToken
	renewedCookie := changer.client.Jar.Cookies(target)[0].Value

	// Each runs twice: first while the outdated row exists, then once it has gone.
	for _, stale := range []struct {
		name, method, path, csrf string
		cookie                   *http.Cookie
		status                   int
	}{
		{"session", "GET", "/api/v1/session", "", guestCookie, 200},
		{"read", "GET", "/api/v1/workspaces/" + workspace.String() + "/projects", "", readerCookie, 401},
		{"mutation", "POST", "/api/v1/auth/logout", writer.csrf, writerCookie, 403},
	} {
		for _, row := range []string{"outdated", "deleted"} {
			if row == "deleted" {
				if tag, err := f.st.Pool.Exec(context.Background(), "DELETE FROM sessions WHERE token=$1", hashedSessionToken(stale.cookie.Value)); err != nil || tag.RowsAffected() != 1 {
					t.Fatalf("delete %s row: %v, %v", stale.name, tag, err)
				}
			}
			response, data := send(stale.method, stale.path, stale.csrf, stale.cookie, "")
			if response.StatusCode != stale.status || len(response.Cookies()) != 0 {
				t.Fatalf("%s with %s row: status %d, cookies %v: %s", stale.name, row, response.StatusCode, response.Cookies(), data)
			}
			if strings.Contains(string(data), f.owner.String()) {
				t.Fatalf("%s with %s row returned the account: %s", stale.name, row, data)
			}
		}
	}
	if v := changer.session(); v.User == nil || v.User.Id != f.owner || changer.client.Jar.Cookies(target)[0].Value != renewedCookie {
		t.Fatal("renewed session did not survive stale requests")
	}

	// A browser left holding the dead token can still sign in, but only with its CSRF token.
	if v := reader.session(); v.User != nil || v.CsrfToken == "" || reader.client.Jar.Cookies(target)[0].Value != readerCookie.Value {
		t.Fatalf("dead token session: %+v", v)
	}
	token := reader.csrf
	reader.csrf = "not the token"
	reader.call("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.test", "password": replacement}, 403, nil)
	reader.csrf = token
	if v := reader.login("owner@example.test", replacement); v.User == nil || reader.client.Jar.Cookies(target)[0].Value == readerCookie.Value {
		t.Fatal("sign-in from a dead token did not replace it")
	}

	changer.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	if cookies := changer.client.Jar.Cookies(target); len(cookies) != 0 {
		t.Fatalf("logout kept the cookie: %v", cookies)
	}
}

// A dropped connection or statement timeout on the account read must not sign out a
// valid session, while a missing account must still end it.
func TestBrowserSessionEndsOnlyWithItsAccount(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := f.browser(t)
	workspace := b.login("owner@example.test", f.password).Workspaces[0].Id
	var token string
	if err := f.st.Pool.QueryRow(ctx, "SELECT token FROM sessions").Scan(&token); err != nil {
		t.Fatal(err)
	}

	if _, err := f.st.Pool.Exec(ctx, "ALTER TABLE accounts RENAME TO accounts_unavailable"); err != nil {
		t.Fatal(err)
	}
	b.call("GET", "/api/v1/workspaces/"+workspace.String()+"/projects", nil, 500, nil)
	if _, err := f.st.Pool.Exec(ctx, "ALTER TABLE accounts_unavailable RENAME TO accounts"); err != nil {
		t.Fatal(err)
	}
	if v := b.session(); v.User == nil || v.User.Id != f.owner {
		t.Fatal("session did not survive the failed account lookup")
	}

	// The product has no account deletion, so remove the row past its references.
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM accounts WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if v := b.session(); v.User != nil {
		t.Fatal("session outlived its deleted account")
	}
	var rows int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE token=$1", token).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("deleted account left its session row", err)
	}
}
