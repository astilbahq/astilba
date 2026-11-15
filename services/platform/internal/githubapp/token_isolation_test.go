package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// revokingGitHub mints a distinct token each time and, like GitHub, lets a token's holder
// revoke it with DELETE /installation/token, answering 401 for it after.
type revokingGitHub struct {
	minted, appCalls atomic.Int64
	mu               sync.Mutex
	revoked          map[string]bool
	// limit, when set, answers every App request with it, and limitRepository the repository's
	// installation-token request.
	limit, limitRepository func(w http.ResponseWriter)
	repositoryCalls        atomic.Int64
}

func (g *revokingGitHub) client(t *testing.T) *Client {
	t.Helper()
	g.revoked = map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/app", "/repos/team/app/installation", "/app/installations/2/access_tokens":
			g.appCalls.Add(1)
			if g.limit != nil {
				g.limit(w)
				return
			}
		default:
			if r.URL.Path == "/repos/team/app" {
				g.repositoryCalls.Add(1)
				if g.limitRepository != nil {
					g.limitRepository(w)
					return
				}
			}
			g.mu.Lock()
			revoked := g.revoked[token]
			g.mu.Unlock()
			if revoked || !strings.HasPrefix(token, "ghs_") {
				w.WriteHeader(401)
				return
			}
		}
		switch r.URL.Path {
		case "/app":
			stablejson.Encode(w, map[string]any{"id": 1, "client_id": "fixture-client"})
		case "/repos/team/app/installation":
			stablejson.Encode(w, map[string]any{"id": 2, "app_id": 1, "permissions": map[string]string{"contents": "write", "pull_requests": "write"}})
		case "/repos/team/app":
			stablejson.Encode(w, map[string]any{"id": 12, "full_name": "team/app", "private": true})
		case "/installation/token":
			g.mu.Lock()
			g.revoked[token] = true
			g.mu.Unlock()
			w.WriteHeader(204)
		case "/app/installations/2/access_tokens":
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &in)
			stablejson.Encode(w, map[string]any{"token": fmt.Sprintf("ghs_%036d", g.minted.Add(1)), "expires_at": time.Now().Add(time.Hour), "repositories": []map[string]any{{"id": 12}}, "permissions": in.Permissions})
		default:
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	c, e := New(Config{ID: 1, ClientID: "fixture-client", Slug: "fixture", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: server.URL, WebOrigin: server.URL}, "http://127.0.0.1", true)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

// As a runner, or code its build runs, can.
func (g *revokingGitHub) revoke(t *testing.T, c *Client, token string) {
	t.Helper()
	if e := c.request(context.Background(), "DELETE", c.api, "/installation/token", token, nil, nil); e != nil {
		t.Fatal(e)
	}
}

func (g *revokingGitHub) works(c *Client, token string) bool {
	return c.request(context.Background(), "GET", c.api, "/repos/team/app", token, nil, nil) == nil
}

// A runner's token is its own: revoking it leaves management's token, and every other
// runner's, working.
func TestRunnerTokensAreNotShared(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{}
	c := g.client(t)
	own, e := c.Mint(ctx, 2, 12, false)
	if e != nil {
		t.Fatal(e)
	}
	workspace, until := uuid.New(), time.Now().Add(50*time.Minute)
	first, expires, e := c.MintRunnerClone(ctx, workspace, uuid.New(), 2, 12, until)
	if e != nil || time.Until(expires) < 55*time.Minute {
		t.Fatal("a runner's token is not fresh", expires, e)
	}
	second, _, e := c.MintRunnerClone(ctx, workspace, uuid.New(), 2, 12, until)
	if e != nil || first == own || second == own || first == second || g.minted.Load() != 3 {
		t.Fatal("runners were given a token someone else holds", g.minted.Load(), e)
	}
	g.revoke(t, c, first)
	if again, e := c.Mint(ctx, 2, 12, false); e != nil || again != own || g.minted.Load() != 3 {
		t.Fatal("a runner's token reached management's cache", e)
	}
	if e = c.Validate(ctx, 2, 12, 1, "team/app", true); e != nil {
		t.Fatal("management's check failed after a runner revoked its token", e)
	}
	if g.works(c, first) || !g.works(c, second) || !g.works(c, own) {
		t.Fatal("revoking one runner's token reached another's")
	}
}

// A cached token GitHub stops accepting is dropped, so the next call mints another. Forget
// drops only that token.
func TestRejectedTokenIsMintedAgain(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{}
	c := g.client(t)
	own, e := c.Mint(ctx, 2, 12, false)
	if e != nil {
		t.Fatal(e)
	}
	g.revoke(t, c, own)
	if e = c.Validate(ctx, 2, 12, 1, "team/app", true); !Unauthorized(e) || !errors.Is(e, ErrTokenRejected) {
		t.Fatal("a revoked token passed the check", e)
	}
	next, e := c.Mint(ctx, 2, 12, false)
	if e != nil || next == own || g.minted.Load() != 2 {
		t.Fatal("a token GitHub rejected stayed cached", e)
	}
	if e = c.Validate(ctx, 2, 12, 2, "team/app", true); e != nil {
		t.Fatal("the check did not recover with a new token", e)
	}
	c.Forget(own)
	if again, e := c.Mint(ctx, 2, 12, false); e != nil || again != next || g.minted.Load() != 2 {
		t.Fatal("forgetting an old token dropped the newer one", e)
	}
	c.Forget(next)
	if again, e := c.Mint(ctx, 2, 12, false); e != nil || again == next || g.minted.Load() != 3 {
		t.Fatal("a forgotten token was reused", e)
	}
}

// GitHub's rate limits are told apart from other refusals, and once GitHub limits the App,
// its requests wait out the time GitHub gives without asking again.
func TestRateLimitsPauseAppRequests(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		until   time.Duration
		limited bool
	}{
		{name: "retry-after", status: 403, headers: map[string]string{"Retry-After": "30"}, until: 30 * time.Second, limited: true},
		{name: "primary limit reset", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(10 * time.Minute).Unix())}, until: 10 * time.Minute, limited: true},
		{name: "secondary limit without headers", status: 403, body: `{"message":"You have exceeded a secondary rate limit."}`, until: time.Minute, limited: true},
		{name: "429 without headers", status: 429, until: time.Minute, limited: true},
		{name: "reset beyond the hour", status: 429, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(48 * time.Hour).Unix())}, until: time.Hour, limited: true},
		{name: "reset already passed", status: 429, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(-time.Minute).Unix())}, until: time.Second, limited: true},
		{name: "forbidden", status: 403, body: `{"message":"Resource not accessible by integration"}`},
		{name: "remaining requests", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "12", "X-RateLimit-Reset": fmt.Sprint(now.Add(time.Minute).Unix())}},
		{name: "unauthorized", status: 401, headers: map[string]string{"Retry-After": "30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: http.NoBody}
			for k, v := range tc.headers {
				res.Header.Set(k, v)
			}
			if tc.body != "" {
				res.Body = readCloser{strings.NewReader(tc.body)}
			}
			until, limited := rateLimit(res, now)
			if limited != tc.limited || limited && !until.Equal(now.Add(tc.until)) {
				t.Fatalf("got %v until %v", limited, until.Sub(now))
			}
		})
	}
	ctx := context.Background()
	g := &revokingGitHub{limit: func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}}
	c := g.client(t)
	_, _, e := c.MintRunnerClone(ctx, uuid.New(), uuid.New(), 2, 12, time.Now())
	until, limited := Limited(e)
	if !limited || time.Until(until) < 50*time.Second {
		t.Fatal("a rate limit was not reported as one", e)
	}
	if _, e = c.Mint(ctx, 2, 12, false); !isLimited(e) {
		t.Fatal("a paused App minted", e)
	}
	if e = c.Authority(ctx, 2, "team/app"); !isLimited(e) || Revoked(e) {
		t.Fatal("a paused App checked its authority", e)
	}
	if g.appCalls.Load() != 1 {
		t.Fatal("the App asked GitHub again while limited", g.appCalls.Load())
	}
	g.limit = nil
	c.mu.Lock()
	c.paused = time.Now()
	c.mu.Unlock()
	if _, _, e = c.MintRunnerClone(ctx, uuid.New(), uuid.New(), 2, 12, time.Now()); e != nil {
		t.Fatal("the App did not resume after the limit", e)
	}
}

func isLimited(err error) bool {
	_, limited := Limited(err)
	return limited
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

// A rate limit on an installation-token request is the check's answer until GitHub's time, so a
// delivery that waits on it doesn't ask GitHub again every second.
func TestALimitedRepositoryCheckIsNotAskedAgainEarly(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{limitRepository: func(w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Minute).Unix()))
		w.WriteHeader(403)
	}}
	c := g.client(t)
	for range 3 {
		if e := c.Validate(ctx, 2, 12, 1, "team/app", true); !isLimited(e) {
			t.Fatal("a limited check was not reported as one", e)
		}
	}
	if g.repositoryCalls.Load() != 1 || g.appCalls.Load() != 3 {
		t.Fatalf("a limited check asked GitHub again: repository=%d app=%d", g.repositoryCalls.Load(), g.appCalls.Load())
	}
	// Another grant generation's check is its own.
	if e := c.Validate(ctx, 2, 12, 2, "team/app", true); !isLimited(e) || g.repositoryCalls.Load() != 2 {
		t.Fatal("a different check reused another's limit", e)
	}
}

// A pipeline's retries and redeliveries get its one token while it lasts, so a runner can't make
// management mint more by asking again. One that would expire too soon is replaced.
func TestARunnerTokenServesItsPipelineAgain(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{}
	c := g.client(t)
	workspace, pipeline := uuid.New(), uuid.New()
	until := time.Now().Add(50 * time.Minute)
	first, _, e := c.MintRunnerClone(ctx, workspace, pipeline, 2, 12, until)
	if e != nil {
		t.Fatal(e)
	}
	for range 5 {
		if again, _, e := c.MintRunnerClone(ctx, workspace, pipeline, 2, 12, until); e != nil || again != first {
			t.Fatal("a redelivery minted another token", e)
		}
	}
	if g.minted.Load() != 1 {
		t.Fatal("a pipeline's token was minted again", g.minted.Load())
	}
	if later, _, e := c.MintRunnerClone(ctx, workspace, pipeline, 2, 12, time.Now().Add(2*time.Hour)); e != nil || later == first {
		t.Fatal("a token that expires too soon was reused", e)
	}
}

// One workspace filling its share of the cache gives way only to itself: another workspace's
// pipeline keeps its token.
func TestAWorkspaceCannotDisplaceAnothersRunnerTokens(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{}
	c := g.client(t)
	until := time.Now().Add(50 * time.Minute)
	other, otherPipeline := uuid.New(), uuid.New()
	kept, _, e := c.MintRunnerClone(ctx, other, otherPipeline, 2, 12, until)
	if e != nil {
		t.Fatal(e)
	}
	flood := uuid.New()
	pipelines := make([]uuid.UUID, runnerTokenLimit+8)
	for i := range pipelines {
		pipelines[i] = uuid.New()
		if _, _, e = c.MintRunnerClone(ctx, flood, pipelines[i], 2, 12, until); e != nil {
			t.Fatal(e)
		}
	}
	minted := g.minted.Load()
	if again, _, e := c.MintRunnerClone(ctx, other, otherPipeline, 2, 12, until); e != nil || again != kept || g.minted.Load() != minted {
		t.Fatal("one workspace's pipelines displaced another's token", e)
	}
	c.mu.Lock()
	held := len(c.runners[flood])
	c.mu.Unlock()
	if held != runnerTokenLimit {
		t.Fatal("a workspace's runner tokens are not bounded", held)
	}
	if _, _, e = c.MintRunnerClone(ctx, flood, pipelines[len(pipelines)-1], 2, 12, until); e != nil || g.minted.Load() != minted {
		t.Fatal("a workspace's newest token was displaced", e)
	}
}

// Expired runner tokens are dropped, and a workspace with none left is forgotten.
func TestExpiredRunnerTokensArePruned(t *testing.T) {
	ctx := context.Background()
	g := &revokingGitHub{}
	c := g.client(t)
	gone, kept := uuid.New(), uuid.New()
	if _, _, e := c.MintRunnerClone(ctx, gone, uuid.New(), 2, 12, time.Now()); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	for k, v := range c.runners[gone] {
		v.expires = time.Now().Add(-time.Second)
		c.runners[gone][k] = v
	}
	c.runnersPruned = time.Time{}
	c.mu.Unlock()
	if _, _, e := c.MintRunnerClone(ctx, kept, uuid.New(), 2, 12, time.Now()); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, held := c.runners[gone]; held || len(c.runners[kept]) != 1 {
		t.Fatal("expired runner tokens were kept", len(c.runners))
	}
}
