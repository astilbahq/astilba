package pipeline

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline/pipelinetest"
)

// A runner receives only a credential the Git server says cannot push. The server is asked for
// its receive-pack advertisement, which every provider refuses without write access.
func TestCanPushAsksTheGitServer(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := pipelinetest.NewForge(kind, map[string]pipelinetest.File{"compose.yaml": {Content: "services: {}\n", Mode: "100644"}})
			writer, reader := forgeProvider(t, f, pipelinetest.Writer), forgeProvider(t, f, pipelinetest.Reader)
			if can, err := writer.CanPush(t.Context()); err != nil || !can {
				t.Fatalf("a write credential was taken for read-only: %t %v", can, err)
			}
			if can, err := reader.CanPush(t.Context()); err != nil || can {
				t.Fatalf("a read credential was taken for one that can push: %t %v", can, err)
			}
			// A credential the server didn't accept says nothing about what it could do.
			if _, err := forgeProvider(t, f, "not-a-token").CanPush(t.Context()); !errors.Is(err, ErrCredentialRejected) {
				t.Fatalf("an unaccepted credential was taken for read-only: %v", err)
			}
			// Refused whatever the credential could do, as by an IP allow list or a ban after failed
			// sign-ins, a credential that can push is never taken for read-only.
			f.Blocked = true
			for _, p := range []*Provider{writer, reader} {
				var unclear *PushUnclear
				if can, err := p.CanPush(t.Context()); !errors.As(err, &unclear) || unclear.Status != http.StatusForbidden {
					t.Fatalf("a blocked request was taken for an answer: %t %v", can, err)
				}
			}
			f.Blocked = false
			for status, passes := range map[int]bool{http.StatusInternalServerError: true, http.StatusRequestTimeout: true, http.StatusNotFound: false, http.StatusBadRequest: false} {
				f.Down = status
				var unclear *PushUnclear
				can, err := reader.CanPush(t.Context())
				if err == nil || errors.As(err, &unclear) == passes {
					t.Fatalf("HTTP %d was misread: %t %v", status, can, err)
				}
			}
		})
	}
	// Redirects are not followed, so a redirect to a page that answers 200 proves nothing.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	p, _ := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app"}, Credential{Password: pipelinetest.Reader}, Policy{Development: true})
	if can, err := p.CanPush(t.Context()); err == nil || can {
		t.Fatalf("a redirect was taken for an answer: %t %v", can, err)
	}
}

// A credential is read-only only when the server says it has no write access, in its own words,
// as GitHub, GitLab 19.5 and Gitea 28.3 answer them. A refusal for any other reason, as for an
// archived repository or one scheduled for deletion, a ban, single sign-on the credential was not
// authorized for, or a repository the credential cannot see, says nothing about what it could do
// once that passes.
func TestCanPushReadsOnlyTheServersOwnNo(t *testing.T) {
	scope := `{"message":"token does not have at least one of required scope(s), required=[write:repository], token scope=read:repository","url":"https://gitea.example/api/swagger"}`
	for _, tc := range []struct {
		name, kind string
		status     int
		body       string
		// probe is Gitea's answer to the write request asked of its API, if any.
		probeStatus int
		probe       string
		readOnly    bool
	}{
		{name: "fine-grained token", kind: "github", status: 403, body: "Write access to repository not granted.\n", readOnly: true},
		{name: "classic token", kind: "github", status: 403, body: "Permission to team/app.git denied to someone.\n", readOnly: true},
		{name: "single sign-on", kind: "github", status: 403, body: "The 'team' organization has enabled or enforced SAML SSO. To access this repository, you must re-authorize the OAuth Application.\n"},
		{name: "IP allow list", kind: "github", status: 403, body: "Although you appear to have the correct authorization credentials, the `team` organization has an IP allow list enabled, and your IP address is not permitted to access this resource.\n"},
		{name: "unseen repository", kind: "github", status: 404, body: "Repository not found.\n"},
		{name: "message within another", kind: "github", status: 403, body: "Blocked. Write access to repository not granted.\n"},
		{name: "token without push", kind: "gitlab", status: 403, body: "You are not allowed to upload code.", readOnly: true},
		{name: "caller without a user", kind: "gitlab", status: 403, body: "You are not allowed to upload code for this project.", readOnly: true},
		{name: "push withheld by policy", kind: "gitlab", status: 403, body: "You are not allowed to push code to this project."},
		{name: "fine-grained tokens required", kind: "gitlab", status: 403, body: "Access denied: This operation requires a fine-grained personal access token with the following project permissions: [Repository: Push]."},
		{name: "failed sign-in ban", kind: "gitlab", status: 403, body: "Too many failed authentication attempts from this IP"},
		{name: "archived", kind: "gitlab", status: 403, body: "You can't push code to an archived project."},
		{name: "read-only repository", kind: "gitlab", status: 403, body: "The repository is temporarily read-only. Please try again later."},
		{name: "fine-grained tokens off", kind: "gitlab", status: 403, body: "Access denied: Fine-grained personal access tokens are not yet supported."},
		{name: "unseen project", kind: "gitlab", status: 404, body: "The project you were looking for could not be found or you don't have permission to view it."},
		{name: "token without write scope", kind: "gitea", status: 403, body: "Forbidden\n", probeStatus: 403, probe: scope, readOnly: true},
		{name: "archived with a read token", kind: "gitea", status: 403, body: "This repo is archived. You can view files and clone it, but cannot push or open issues/pull-requests.", probeStatus: 403, probe: scope, readOnly: true},
		{name: "archived with a write token", kind: "gitea", status: 403, body: "This repo is archived. You can view files and clone it, but cannot push or open issues/pull-requests.", probeStatus: 423, probe: `{"message":"repo is archived"}`},
		{name: "allow list in front", kind: "gitea", status: 403, body: "Forbidden\n", probeStatus: 403, probe: "Forbidden\n"},
		{name: "another scope refused", kind: "gitea", status: 403, body: "Forbidden\n", probeStatus: 403, probe: `{"message":"token does not have at least one of required scope(s), required=[write:issue]"}`},
		{name: "API refuses the token", kind: "gitea", status: 403, body: "Forbidden\n", probeStatus: 401, probe: `{"message":"token is required"}`},
		{name: "disabled account", kind: "gitea", status: 403, body: "Your account is disabled.", probeStatus: 403, probe: "Your account is disabled."},
		{name: "unseen repository", kind: "gitea", status: 404, body: "Repository not found"},
	} {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			probed := false
			p := answeringProvider(t, tc.kind, func(w http.ResponseWriter, r *http.Request) {
				if user, password, ok := r.BasicAuth(); !ok || user != "fixture" || password != "clone-token" {
					t.Errorf("asked without the credential as Git presents it: %s", r.URL)
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/team/app.git/info/refs" && r.URL.Query().Get("service") == "git-receive-pack":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				case tc.probe != "" && r.Method == http.MethodDelete && r.URL.Path == "/api/v1/repos/team/app/branches/"+giteaNoBranch:
					probed = true
					w.WriteHeader(tc.probeStatus)
					_, _ = w.Write([]byte(tc.probe))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusTeapot)
				}
			})
			can, err := p.CanPush(t.Context())
			if tc.readOnly {
				if err != nil || can {
					t.Fatalf("the server's no was not read: %t %v", can, err)
				}
				return
			}
			var unclear *PushUnclear
			if !errors.As(err, &unclear) || unclear.Status != tc.status || can {
				t.Fatalf("a refusal that says nothing about the credential was taken for an answer: %t %v", can, err)
			}
			if tc.probe != "" && !probed {
				t.Fatal("Gitea's API was not asked")
			}
		})
	}
}

// A refusal that may pass, as a rate limit or a server error, is neither read-only nor final,
// whether the Git server or Gitea's API gives it.
func TestCanPushPassesWhatMayPass(t *testing.T) {
	for name, refuse := range map[string]http.HandlerFunc{
		"retry-after": func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "30"); w.WriteHeader(403) },
		"body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			_, _ = w.Write([]byte("API rate limit exceeded"))
		},
		"server error": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) },
	} {
		for _, where := range []string{"Git", "API"} {
			t.Run(name+"/"+where, func(t *testing.T) {
				p := answeringProvider(t, "gitea", func(w http.ResponseWriter, r *http.Request) {
					if where == "API" && strings.HasPrefix(r.URL.Path, "/team/") {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
					refuse(w, r)
				})
				var unclear *PushUnclear
				var status *ProviderError
				if can, err := p.CanPush(t.Context()); !errors.As(err, &status) || errors.As(err, &unclear) || can {
					t.Fatalf("a refusal that may pass was taken for an answer: %t %v", can, err)
				}
			})
		}
	}
}

// answeringProvider is a provider of kind for team/app whose server answers with handler, with
// a clone credential.
func answeringProvider(t *testing.T, kind string, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := Connection{Provider: kind, BaseURL: server.URL, Repository: "team/app", RepositoryID: 12, Private: true}
	policy := Policy{Development: true}
	if kind == "github" {
		c.BaseURL = "https://api.github.com"
		policy.GitHubFixture = server.URL
	}
	p, err := NewProvider(c, Credential{Username: "fixture", Password: "clone-token"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
