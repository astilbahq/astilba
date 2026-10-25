package githubapp

import (
	"context"
	"crypto"
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
)

func TestScopedTokensIdentityAndBoundedReuse(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var minted, metadata atomic.Int64
	var broader, removed atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" || strings.HasSuffix(r.URL.Path, "/installation") || strings.HasSuffix(r.URL.Path, "/access_tokens") {
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			if len(parts) != 3 {
				t.Error("missing App JWT")
				w.WriteHeader(401)
				return
			}
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig) != nil {
				t.Error("invalid JWT signature")
			}
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				Iat int64  `json:"iat"`
				Exp int64  `json:"exp"`
				Iss string `json:"iss"`
			}
			_ = json.Unmarshal(raw, &claims)
			if claims.Iss != "fixture-client" || claims.Iat > time.Now().Unix() || claims.Exp > time.Now().Add(10*time.Minute).Unix() {
				t.Error("invalid JWT identity/lifetime")
			}
		}
		switch r.URL.Path {
		case "/app":
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "client_id": "fixture-client"})
		case "/repos/team/app/installation":
			if removed.Load() {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 2, "app_id": 1, "permissions": map[string]string{"contents": "write", "pull_requests": "write"}})
		case "/repos/team/app":
			metadata.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": 12, "full_name": "team/app", "private": true})
		case "/app/installations/2/access_tokens":
			minted.Add(1)
			var in struct {
				Repositories []int64           `json:"repository_ids"`
				Permissions  map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Repositories) != 1 || in.Repositories[0] != 12 {
				t.Error("token was not restricted to selected repository")
			}
			if broader.Load() {
				in.Permissions["administration"] = "write"
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_1_" + strings.Repeat("opaque.", 30), "expires_at": time.Now().Add(time.Hour), "repositories": []map[string]any{{"id": 12}}, "permissions": in.Permissions})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	config := Config{ID: 1, ClientID: "fixture-client", Slug: "fixture-app", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: server.URL, WebOrigin: server.URL}
	c, e := New(config, "http://127.0.0.1:8080", true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = New(config, "https://example.test", false); e == nil {
		t.Fatal("production accepted fixture origins")
	}
	u, _ := url.Parse(c.AuthorizeURL("state", "verifier"))
	challenge := sha256.Sum256([]byte("verifier"))
	if u.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE challenge missing")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if e := c.Validate(context.Background(), 2, 12, 1, "team/app", true); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if minted.Load() != 1 || metadata.Load() != 1 {
		t.Fatalf("repeated/concurrent access amplified calls: mints=%d metadata=%d", minted.Load(), metadata.Load())
	}
	if _, e = c.Mint(context.Background(), 2, 12, true); e != nil {
		t.Fatal(e)
	}
	if minted.Load() != 2 {
		t.Fatal("write and read token scopes were conflated")
	}
	broader.Store(true)
	fresh, e := New(config, "http://127.0.0.1:8080", true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.Mint(context.Background(), 2, 12, false); e == nil {
		t.Fatal("accepted broader token scope")
	}
	broader.Store(false)
	if e = c.Validate(context.Background(), 2, 12, 2, "team/app", false); e == nil {
		t.Fatal("changed visibility reused old validation")
	}
	removed.Store(true)
	if e = c.Validate(context.Background(), 2, 12, 3, "team/app", true); !Revoked(e) {
		t.Fatalf("generation change reused revoked installation: %v", e)
	}
}

type joinedContext struct {
	context.Context
	once   sync.Once
	joined *sync.WaitGroup
}

func (c *joinedContext) Done() <-chan struct{} {
	c.once.Do(c.joined.Done)
	return c.Context.Done()
}

// One failed in-flight attempt belongs to every caller already waiting on it;
// it must not turn a shared outage into one serial API request per runner.
func TestConcurrentFailuresShareAttempt(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"authority", "token"} {
		t.Run(kind, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				w.WriteHeader(503)
			}))
			defer server.Close()
			c, e := New(Config{ID: 1, ClientID: "fixture-client", Slug: "fixture", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: server.URL, WebOrigin: server.URL}, "http://127.0.0.1", true)
			if e != nil {
				t.Fatal(e)
			}
			invoke := func(ctx context.Context) error {
				if kind == "authority" {
					return c.Validate(ctx, 2, 12, 1, "team/app", true)
				}
				_, e := c.Mint(ctx, 2, 12, false)
				return e
			}
			results := make(chan error, 20)
			go func() { results <- invoke(context.Background()) }()
			<-entered
			var ready sync.WaitGroup
			ready.Add(19)
			for range 19 {
				go func() { results <- invoke(&joinedContext{Context: context.Background(), joined: &ready}) }()
			}
			ready.Wait()
			close(release)
			for range 20 {
				if e := <-results; e == nil {
					t.Fatal("upstream failure became success")
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("overlapping failures made %d requests", calls.Load())
			}
			if e = invoke(context.Background()); e == nil || calls.Load() != 2 {
				t.Fatal("later independent request did not retry")
			}
		})
	}
}
