package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPullRequestAuthorityAndDiscovery(t *testing.T) {
	// Provider metadata, not signed webhook claims, supplies execution authority.
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			base, source, state, sha := int64(12), int64(99), "open", strings.Repeat("a", 40)
			if kind == "gitlab" {
				state = "opened"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/7"):
					json.NewEncoder(w).Encode(map[string]any{"number": 7, "iid": 7, "state": state, "sha": sha, "target_project_id": base, "source_project_id": source, "target_branch": "main", "base": map[string]any{"ref": "main", "repo": map[string]any{"id": base}}, "head": map[string]any{"sha": sha, "repo": map[string]any{"id": source}}})
				case strings.HasSuffix(r.URL.Path, "/pulls") || strings.HasSuffix(r.URL.Path, "/merge_requests"):
					if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "50" {
						t.Error("discovery cursor/bound missing")
					}
					json.NewEncoder(w).Encode([]map[string]int{{"number": 7, "iid": 7}})
				default:
					json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": true})
				}
			}))
			defer server.Close()
			origin, policy := server.URL, Policy{Development: true}
			if kind == "github" {
				origin = "https://api.github.com"
				policy.GitHubFixture = server.URL
			}
			p, err := NewProvider(Connection{Provider: kind, BaseURL: origin, Repository: "team/repo", RepositoryID: 12, Private: true}, Credential{}, policy)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			got, err := p.PullRequest(ctx, 7)
			if err != nil || !got.Open || !got.Fork || got.Head != sha || got.HeadRepository != 99 || got.BaseBranch != "main" {
				t.Fatalf("fork identity: %+v %v", got, err)
			}
			rows, err := p.OpenPullRequests(ctx, "main", 2)
			if err != nil || len(rows) != 1 || rows[0] != 7 {
				t.Fatal(rows, err)
			}
			base = 13
			if _, err = p.PullRequest(ctx, 7); err == nil {
				t.Fatal("accepted replaced target identity")
			}
			base = 12
			source = 0
			if _, err = p.PullRequest(ctx, 7); err == nil {
				t.Fatal("accepted missing open source authority")
			}
			source = 99
			sha = "bad"
			if _, err = p.PullRequest(ctx, 7); err == nil {
				t.Fatal("accepted invalid head")
			}
			state = "closed"
			source = 0
			if got, err = p.PullRequest(ctx, 7); err != nil || got.Open {
				t.Fatal("cannot verify closure after source deletion", err)
			}
			state = "unknown"
			if _, err = p.PullRequest(ctx, 7); err == nil {
				t.Fatal("unknown state treated as closed")
			}
		})
	}
}

func TestPullRequestWebhookAuthentication(t *testing.T) {
	secret := strings.Repeat("s", 43)
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte(`{"number":7,"repository":{"id":12},"project":{"id":12},"object_attributes":{"iid":7,"target_project_id":12},"head":{"sha":"untrusted"}}`)
			h := http.Header{}
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			switch kind {
			case "github":
				h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
				h.Set("X-GitHub-Event", "pull_request")
				h.Set("X-GitHub-Delivery", "delivery")
			case "gitea":
				h.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
				h.Set("X-Gitea-Event", "pull_request_sync")
				h.Set("X-Gitea-Delivery", "delivery")
			case "gitlab":
				h.Set("X-Gitlab-Token", secret)
				h.Set("X-Gitlab-Event", "Merge Request Hook")
				h.Set("webhook-id", "delivery")
			}
			got, err := VerifyPullRequest(kind, secret, 12, h, body)
			if err != nil || got.Number != 7 || got.Delivery != "delivery" {
				t.Fatal(got, err)
			}
			if _, err = VerifyPullRequest(kind, secret, 13, h, body); err == nil {
				t.Fatal("accepted foreign repository")
			}
			if _, err = VerifyPullRequest(kind, secret, 12, h, append(body, 'x')); err == nil {
				t.Fatal("accepted modified body")
			}
			if _, err = VerifyPullRequest(kind, strings.Repeat("z", 43), 12, h, body); err == nil {
				t.Fatal("accepted forged signature")
			}
			if _, err = VerifyPush(kind, secret, 12, h, body); err == nil {
				t.Fatal("request event entered push processing")
			}
		})
	}
}
