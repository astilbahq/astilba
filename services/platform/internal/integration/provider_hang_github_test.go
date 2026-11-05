package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// heldGitHub answers the OAuth and repository calls of connecting a GitHub App, holding the
// requests to one path until released.
type heldGitHub struct {
	b            *browser
	prefix       string
	previewMints atomic.Int64
	challenge    atomic.Value
	mu           sync.Mutex
	match        string
	gate         chan struct{}
	entered      chan struct{}
}

func heldGitHubFor(t *testing.T, f *fixture, b *browser, ws uuid.UUID) *heldGitHub {
	t.Helper()
	h := &heldGitHub{b: b, prefix: "/api/v1/workspaces/" + ws.String()}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		gate, entered := h.gate, h.entered
		held := gate != nil && r.URL.Path == h.match
		h.mu.Unlock()
		if held {
			entered <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != h.challenge.Load() {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "user-token-canary"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "repository-admin"})
		case "/user/installations/2/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"id": 12, "full_name": "team/app", "private": true, "permissions": map[string]bool{"admin": true}}}})
		case "/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "client_id": "fixture-client"})
		case "/repos/team/app/installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2, "app_id": 1, "permissions": map[string]string{"contents": "write", "pull_requests": "write"}})
		case "/repos/team/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 12, "full_name": "team/app", "private": true, "permissions": map[string]bool{"admin": true}})
		case "/repos/team/app/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
		case "/app/installations/2/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Permissions["pull_requests"] != "" {
				h.previewMints.Add(1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-" + in.Permissions["contents"] + "-canary", "expires_at": time.Now().Add(time.Hour), "repositories": []map[string]int{{"id": 12}}, "permissions": in.Permissions})
		case "/applications/fixture-client/token":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(provider.Close)
	app, err := githubapp.New(githubapp.Config{ID: 1, ClientID: "fixture-client", Slug: "fixture-app", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: provider.URL, WebOrigin: provider.URL}, f.server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	f.execution.GitHub = app
	f.execution.Policy.GitHubFixture = provider.URL
	return h
}

func (h *heldGitHub) hold(t *testing.T, path string) (<-chan struct{}, func()) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 8)
	h.mu.Lock()
	h.match, h.gate, h.entered = path, gate, entered
	h.mu.Unlock()
	release := sync.OnceFunc(func() {
		h.mu.Lock()
		h.gate = nil
		h.mu.Unlock()
		close(gate)
	})
	t.Cleanup(release)
	return entered, release
}

func (h *heldGitHub) start(t *testing.T) string {
	t.Helper()
	var start api.GitHubAuthorizationStart
	h.b.call("POST", h.prefix+"/github-app/authorizations", nil, 200, &start)
	u, err := url.Parse(start.AuthorizationUrl)
	if err != nil {
		t.Fatal(err)
	}
	h.challenge.Store(u.Query().Get("code_challenge"))
	return u.Query().Get("state")
}

// connect finishes the authorization and returns the App connection it made.
func (h *heldGitHub) connect(t *testing.T) uuid.UUID {
	t.Helper()
	state := h.start(t)
	var grant execution.GitHubAuthorization
	h.b.call("POST", "/api/v1/github-app/complete", map[string]string{"state": state, "code": "fixture-code"}, 200, &grant)
	var connection execution.ConnectionSummary
	h.b.call("POST", h.prefix+"/github-app/authorizations/"+grant.ID.String()+"/connections", map[string]any{"name": "App repository", "repository": "team/app", "repositoryId": 12, "installationId": 2}, 201, &connection)
	return connection.ID
}

// Validating a GitHub App grant is the longest call on the delivery path, and a token is
// minted behind it. A grant that changed during it must stop that token being delivered.
func TestAppDeliveryRechecksTheGrantAfterAHungValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during string
	}{
		{name: "nothing changed"},
		{name: "grant revoked", during: "UPDATE github_app_connections SET active=false WHERE connection_id=$1"},
		{name: "grant moved to another installation", during: "UPDATE github_app_connections SET installation_id=3 WHERE connection_id=$1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			github := heldGitHubFor(t, h.f, h.b, h.ws)
			h.connection.ID = github.connect(t)
			config := h.config
			config.ConnectionID = h.connection.ID
			op := h.createBuild(t, config)
			// A new generation misses the five-second cache of the admission's validation.
			execSQL(t, h, "UPDATE github_app_connections SET generation=generation+1 WHERE connection_id=$1", h.connection.ID)
			entered, release := github.hold(t, "/repos/team/app/installation")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			result := h.pollAsync(ctx)
			waitEntered(t, entered)
			h.requireNothingHeld(t, op.Id)
			if tc.during != "" {
				execSQL(t, h, tc.during, h.connection.ID)
			}
			release()
			outcome := <-result
			delivered := outcome.command != nil && outcome.command.Pipeline != nil && strings.Contains(string(*outcome.command.Pipeline), "installation-read-canary")
			if tc.during != "" {
				if outcome.err == nil || delivered || h.deliveryStarted(t, op.Id) {
					t.Fatal("a token minted for the old grant was delivered", outcome.err)
				}
				return
			}
			if outcome.err != nil || !delivered || !h.deliveryStarted(t, op.Id) {
				t.Fatal("an unchanged App delivery was lost", outcome.err)
			}
		})
	}
}

// A policy's discovery reads pull requests, so its save has to check the branch with a token
// that can, or an App without that permission saves a policy that never discovers anything.
func TestPolicySaveChecksTheBranchWithThePullRequestToken(t *testing.T) {
	h := newHungPreview(t)
	github := heldGitHubFor(t, h.f, h.b, h.ws)
	build := h.policy["build"].(pipeline.BuildConfig)
	build.ConnectionID = github.connect(t)
	h.policy["build"] = build
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": false, "generation": 0, "configuration": h.policy}, 200, nil)
	if github.previewMints.Load() != 1 {
		t.Fatal("the branch check did not use the pull request token", github.previewMints.Load())
	}
}
