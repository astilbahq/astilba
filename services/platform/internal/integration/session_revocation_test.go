package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/httpapi"
)

type heldSessionStore struct {
	scs.CtxStore
	armed   atomic.Bool
	loaded  chan struct{}
	release chan struct{}
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
			decoder := json.NewDecoder(bytes.NewReader(result.body))
			var failure api.Error
			if err := decoder.Decode(&failure); err != nil || failure.Code != "unauthenticated" {
				t.Fatal("stale response did not contain the authentication error", err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
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
