package pipeline

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline/pipelinetest"
	"github.com/google/uuid"
)

func forgeProvider(t *testing.T, f *pipelinetest.Forge, password string) *Provider {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	c := Connection{Provider: f.Kind(), BaseURL: server.URL, Repository: "team/app", RepositoryID: 12, Private: true}
	policy := Policy{Development: true}
	if f.Kind() == "github" {
		c.BaseURL = "https://api.github.com"
		policy.GitHubFixture = server.URL
	}
	p, err := NewProvider(c, Credential{Username: "fixture", Password: password}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Management proposes a draft through each provider's API: one commit on a create-only branch,
// with only the draft's changes, then one review. A retry finds both again, and a branch of the
// same name holding anything else is refused rather than proposed under Astilba's name.
func TestSubmitThroughEachProvider(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			f, base := pipelinetest.NewForge(kind, map[string]pipelinetest.File{
				"compose.yaml":   {Content: "services: {}\n", Mode: "100644"},
				"deploy/run.sh":  {Content: "#!/bin/sh\n", Mode: "100755"},
				"deploy/old.yml": {Content: "old: true\n", Mode: "100644"},
				"README.md":      {Content: "keep me\n", Mode: "100644"},
			})
			p := forgeProvider(t, f, pipelinetest.Writer)
			s := Submission{ID: uuid.New(), Base: base, Branch: "main", At: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
				Previous: []string{"compose.yaml", "deploy/run.sh", "deploy/old.yml"},
				Files:    []composeproject.File{{Path: "compose.yaml", Content: "services: {web: {}}\n"}, {Path: "deploy/run.sh", Content: "#!/bin/sh\necho changed\n"}, {Path: "deploy/new.yml", Content: "new: true\n"}}}
			result, err := p.Submit(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			branch := "astilba/" + s.ID.String()
			if f.Branches[branch] != result.Commit || f.Branches["main"] != base || result.Review.Number != f.Reviews[branch] {
				t.Fatalf("not one branch and review: %+v", result)
			}
			got := f.Commits[result.Commit]
			want := map[string]pipelinetest.File{"compose.yaml": {Content: "services: {web: {}}\n", Mode: "100644"}, "deploy/run.sh": {Content: "#!/bin/sh\necho changed\n", Mode: "100755"}, "deploy/new.yml": {Content: "new: true\n", Mode: "100644"}, "README.md": {Content: "keep me\n", Mode: "100644"}}
			if !slices.Equal(got.Parents, []string{base}) || !maps.Equal(got.Files, want) || !strings.Contains(got.Message, "Astilba-Submission: "+s.ID.String()) {
				t.Fatalf("the commit is not exactly the draft on its base: %+v", got)
			}
			writes := len(f.Writes)
			again, err := p.Submit(t.Context(), s)
			if err != nil || again != result || len(f.Writes) != writes {
				t.Fatalf("a retry did not find the same branch and review: %+v %v %v", again, err, f.Writes[writes:])
			}

			// A branch of the submission's name that holds another change.
			other := s
			other.ID = uuid.New()
			// Exactly this draft's change, and one more file.
			files := maps.Clone(want)
			files["extra.txt"] = pipelinetest.File{Content: "not ours\n", Mode: "100644"}
			f.Branches["astilba/"+other.ID.String()] = f.AddCommit(pipelinetest.Commit{Parents: []string{base}, Message: other.message(), Files: files})
			var refused *SubmissionRefused
			if _, err = p.Submit(t.Context(), other); !errors.As(err, &refused) || f.Reviews["astilba/"+other.ID.String()] != 0 {
				t.Fatalf("a branch holding another change was proposed: %v", err)
			}

			unchanged := s
			unchanged.ID = uuid.New()
			unchanged.Files = []composeproject.File{{Path: "compose.yaml", Content: "services: {}\n"}, {Path: "deploy/run.sh", Content: "#!/bin/sh\n"}, {Path: "deploy/old.yml", Content: "old: true\n"}}
			if _, err = p.Submit(t.Context(), unchanged); !errors.As(err, &refused) {
				t.Fatalf("a draft without changes was submitted: %v", err)
			}

			moved := s
			moved.ID = uuid.New()
			f.Branches["main"] = f.AddCommit(pipelinetest.Commit{Parents: []string{base}, Message: "moved", Files: f.Commits[base].Files})
			if _, err = p.Submit(t.Context(), moved); !errors.As(err, &refused) || f.Branches["astilba/"+moved.ID.String()] != "" {
				t.Fatalf("a draft was submitted onto a moved branch: %v", err)
			}
			if SubmissionRetryable(err) {
				t.Fatal("a moved branch was taken for a passing failure")
			}
			f.Down = http.StatusBadGateway
			if _, err = p.Submit(t.Context(), moved); err == nil || !SubmissionRetryable(err) {
				t.Fatalf("a failing provider was not retried: %v", err)
			}
			if kind == "gitea" {
				// Gitea before 1.20 starts a branch at the default branch whatever it is asked.
				f.Down, f.Version = 0, "1.19.4"
				old := s
				old.ID = uuid.New()
				f.Branches["main"] = base
				_, err = p.Submit(t.Context(), old)
				if !errors.As(err, &refused) || !strings.Contains(err.Error(), "Gitea 1.20 or later") || f.Branches["astilba/"+old.ID.String()] != "" {
					t.Fatalf("an old Gitea was written to: %v", err)
				}
			}
		})
	}
}

// A runner receives only a credential the Git server will not let push. The server is asked for
// its receive-pack advertisement, which every provider refuses without write access.
func TestCanPushAsksTheGitServer(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := pipelinetest.NewForge(kind, map[string]pipelinetest.File{"compose.yaml": {Content: "services: {}\n", Mode: "100644"}})
			if can, err := forgeProvider(t, f, pipelinetest.Writer).CanPush(t.Context()); err != nil || !can {
				t.Fatalf("a write credential was taken for read-only: %t %v", can, err)
			}
			reader := forgeProvider(t, f, pipelinetest.Reader)
			if can, err := reader.CanPush(t.Context()); err != nil || can {
				t.Fatalf("a read credential was taken for one that can push: %t %v", can, err)
			}
			// A credential the server didn't accept says nothing about what it could do.
			if _, err := forgeProvider(t, f, "not-a-token").CanPush(t.Context()); !errors.Is(err, ErrCredentialRejected) {
				t.Fatalf("an unaccepted credential was taken for read-only: %v", err)
			}
			f.Down = http.StatusInternalServerError
			if _, err := reader.CanPush(t.Context()); err == nil {
				t.Fatal("an unknown answer was taken for read-only")
			}
		})
	}
	// Redirects are not followed, so a redirect to a page that answers 200 proves nothing.
	server := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer server.Close()
	p, _ := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app"}, Credential{Password: pipelinetest.Reader}, Policy{Development: true})
	if _, err := p.CanPush(context.Background()); err == nil {
		t.Fatal("a redirect was taken for an answer")
	}
}
