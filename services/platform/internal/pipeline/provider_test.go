package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

func TestProviderAuthorityAndPushAuthentication(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository":{"id":12},"project":{"id":12}}`)
	secret := strings.Repeat("s", 43)
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			headers := http.Header{}
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write(body)
			switch kind {
			case "github":
				headers.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
				headers.Set("X-GitHub-Event", "push")
				headers.Set("X-GitHub-Delivery", "one")
			case "gitea":
				headers.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
				headers.Set("X-Gitea-Event", "push")
				headers.Set("X-Gitea-Delivery", "one")
			case "gitlab":
				headers.Set("X-Gitlab-Token", secret)
				headers.Set("X-Gitlab-Event", "Push Hook")
				headers.Set("webhook-id", "one")
			}
			push, err := VerifyPush(kind, secret, 12, headers, body)
			if err != nil || push.Branch != "main" || push.Commit != strings.Repeat("a", 40) {
				t.Fatal("valid delivery rejected", err)
			}
			for _, invalid := range []struct {
				body []byte
				repo int64
			}{{append(append([]byte{}, body...), 123), 12}, {body, 99}} {
				if _, err = VerifyPush(kind, secret, invalid.repo, headers, invalid.body); err == nil {
					t.Fatal("accepted changed body or fork repository")
				}
			}
			if _, err = VerifyPush(kind, strings.Repeat("x", 43), 12, headers, body); err == nil {
				t.Fatal("accepted wrong signing secret")
			}
		})
	}
	for _, origin := range []string{"https://gitlab.com/path", "http://gitlab.com", "https://user:pass@gitlab.com", "https://unconfigured.example", "https://gitlab.com/?x=1"} {
		if _, err := (Policy{}).Allowed(origin); err == nil {
			t.Fatal("accepted unconfigured provider", origin)
		}
	}
}

func TestProviderExactHeadReviewAndRedirectBoundary(t *testing.T) {
	for _, kind := range []string{"github", "gitea", "gitlab"} {
		t.Run(kind, func(t *testing.T) {
			head := strings.Repeat("a", 40)
			repositoryID := int64(12)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				if kind == "gitlab" {
					auth = r.Header.Get("PRIVATE-TOKEN")
				}
				if !strings.Contains(auth, "fixture-token") {
					t.Error("API credential missing")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(r.URL.Path, "/branches/"):
					if kind == "github" {
						_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": head}})
					} else {
						_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"id": head}})
					}
				case strings.Contains(r.URL.Path, "/pulls/"):
					// Gitea's lookup of a branch's request, when there is none.
					w.WriteHeader(404)
				case strings.HasSuffix(r.URL.Path, "/pulls") || strings.HasSuffix(r.URL.Path, "/merge_requests"):
					if r.Method == "GET" {
						_, _ = w.Write([]byte("[]"))
						return
					}
					_ = stablejson.Encode(w, map[string]any{"number": 5, "iid": 5, "html_url": "https://example.test/review/5", "web_url": "https://example.test/review/5"})
				default:
					_ = stablejson.Encode(w, map[string]any{"id": repositoryID, "private": true, "owner": map[string]string{"visibility": "public"}})
				}
			}))
			defer server.Close()
			baseURL := server.URL
			if kind == "github" {
				baseURL = "https://api.github.com"
			}
			p, err := NewProvider(Connection{Provider: kind, BaseURL: baseURL, Repository: "team/repo", RepositoryID: 12, Private: true}, Credential{Password: "fixture-token"}, Policy{Development: true})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "github" {
				// Keep GitHub's fixed origin while directing only this contract fixture's HTTP transport locally.
				target, _ := url.Parse(server.URL)
				p.client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
					copy := r.Clone(r.Context())
					endpoint := *r.URL
					endpoint.Scheme = target.Scheme
					endpoint.Host = target.Host
					copy.URL = &endpoint
					if r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.Header.Get("Authorization") != "Bearer fixture-token" {
						t.Error("GitHub API headers differ")
					}
					return http.DefaultTransport.RoundTrip(copy)
				})
			}
			if id, private, err := p.Repository(context.Background()); err != nil || id != 12 || !private {
				t.Fatal(id, private, err)
			}
			if got, err := p.Resolve(context.Background(), "feature/nested"); err != nil || got != head {
				t.Fatal(got, err)
			}
			if got, err := p.Review(context.Background(), "main", "astilba/change"); err != nil || got.Number != 5 {
				t.Fatal(got, err)
			}
			repositoryID = 13
			if _, err := p.Resolve(context.Background(), "main"); err == nil {
				t.Fatal("reused repository path accepted a replacement identity")
			}

		})
	}
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	p, err := NewProvider(Connection{Provider: "gitea", BaseURL: redirect.URL, Repository: "team/repo", RepositoryID: 12, Private: true}, Credential{Password: "credential"}, Policy{Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.Repository(context.Background()); err == nil || reached {
		t.Fatal("provider followed credential redirect")
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A cache of minted credentials learns that the server stopped accepting one only from a 401,
// not from a refusal of what the credential asked for.
func TestProviderReportsARejectedCredential(t *testing.T) {
	for status, rejected := range map[int]bool{401: true, 403: false, 404: false, 500: false} {
		p := answering(t, "github", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		var calls int
		p.OnRejected(func() { calls++ })
		if _, err := p.Resolve(context.Background(), "main"); err == nil {
			t.Fatal(status, "succeeded")
		}
		if (calls == 1) != rejected || calls > 1 {
			t.Fatalf("HTTP %d reported a rejection %d times", status, calls)
		}
	}
}

// A provider's refusal says whether it was under the provider's rate limits, which pass, and a
// permanent redirect, as for a renamed repository, is an answer with its status rather than a
// failed request. A temporary redirect is still not followed or believed.
func TestProviderTellsRateLimitsAndMovesApart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  http.HandlerFunc
		status  int
		limited bool
	}{
		{name: "forbidden", answer: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }, status: 403},
		{name: "retry-after", answer: func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "30"); w.WriteHeader(403) }, status: 403, limited: true},
		{name: "secondary limit", answer: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
		}, status: 403, limited: true},
		{name: "too many requests", answer: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, status: 429, limited: true},
		{name: "not found", answer: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, status: 404},
		{name: "moved permanently", answer: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/repositories/1", http.StatusMovedPermanently)
		}, status: 301},
		{name: "permanent redirect", answer: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/repositories/1", http.StatusPermanentRedirect)
		}, status: 308},
		{name: "found", answer: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/repositories/1", http.StatusFound) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := answering(t, "github", tc.answer).Repository(context.Background())
			var status *ProviderError
			if tc.status == 0 {
				if err == nil || errors.As(err, &status) {
					t.Fatal("a temporary redirect was taken for an answer", err)
				}
				return
			}
			if !errors.As(err, &status) || status.Status != tc.status || status.Limited != tc.limited {
				t.Fatalf("got %#v, want HTTP %d limited %v", err, tc.status, tc.limited)
			}
		})
	}
}

// Management's configuration refuses a provider it does not allow, which only the operator's change
// undoes, distinctly from a base URL that is never valid. NewProvider refuses what Check does.
func TestPolicyCheckMarksTheOperatorsRefusal(t *testing.T) {
	gitea := func(base string) Connection {
		return Connection{Provider: "gitea", BaseURL: base, Repository: "team/app"}
	}
	github := Connection{Provider: "github", BaseURL: "https://api.github.com", Repository: "team/app"}
	for _, tc := range []struct {
		name       string
		policy     Policy
		connection Connection
		refused    bool
	}{
		{name: "origin removed", policy: Policy{Origins: []string{"https://other.example"}}, connection: gitea("https://git.example"), refused: true},
		{name: "plain HTTP outside development", policy: Policy{}, connection: gitea("http://127.0.0.1:3000"), refused: true},
		{name: "GitHub fixture outside development", policy: Policy{GitHubFixture: "http://127.0.0.1:9"}, connection: github, refused: true},
		{name: "configured origin", policy: Policy{Origins: []string{"https://git.example"}}, connection: gitea("https://git.example")},
		{name: "public provider", policy: Policy{}, connection: github},
		{name: "loopback in development", policy: Policy{Development: true}, connection: gitea("http://127.0.0.1:3000")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Check(tc.connection)
			if errors.Is(err, ErrNotAllowed) != tc.refused || (err != nil) != tc.refused {
				t.Fatalf("checked with %v, refused %v", err, tc.refused)
			}
			if _, err := NewProvider(tc.connection, Credential{}, tc.policy); (err != nil) != tc.refused || errors.Is(err, ErrNotAllowed) != tc.refused {
				t.Fatalf("NewProvider answered %v, refused %v", err, tc.refused)
			}
		})
	}
	if err := (Policy{Development: true}).Check(gitea("https://git.example/")); err == nil || errors.Is(err, ErrNotAllowed) {
		t.Fatal("a base URL that is never valid was taken for the operator's refusal:", err)
	}
}
