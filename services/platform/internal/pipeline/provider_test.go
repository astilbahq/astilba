package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
					if kind == "gitlab" {
						_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"id": head}})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": head}})
					}
				case strings.HasSuffix(r.URL.Path, "/pulls") || strings.HasSuffix(r.URL.Path, "/merge_requests"):
					if r.Method == "GET" {
						_, _ = w.Write([]byte("[]"))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"number": 5, "iid": 5, "html_url": "https://example.test/review/5", "web_url": "https://example.test/review/5"})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"id": repositoryID, "private": true})
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
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
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
