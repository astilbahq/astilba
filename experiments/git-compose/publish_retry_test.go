// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const missingGitLabSource = `{"message":{"source_branch":["does not exist"]}}`

// The fixture exercises real Git ref updates; only the remote HTTP API is fake.
func publishFixture(t *testing.T, kind string, handler http.HandlerFunc) (*Repository, *Repository, Draft) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewProvider(kind, server.URL, "fixture", "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	local := newGitRepository(provider, t.TempDir(), "fixture/repo", "main")
	remote := newGitRepository(provider, t.TempDir(), "fixture/repo", "main")
	for _, repo := range []*Repository{local, remote} {
		for i, variable := range repo.env {
			if strings.HasPrefix(variable, "GIT_ALLOW_PROTOCOL=") {
				repo.env[i] = "GIT_ALLOW_PROTOCOL=file"
			}
		}
		if _, err := repo.git(t.Context(), nil, "init", "--bare", "--quiet"); err != nil {
			t.Fatal(err)
		}
	}
	git := func(input []byte, args ...string) string {
		t.Helper()
		out, err := local.git(t.Context(), input, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	git(nil, "remote", "add", "origin", remote.dir)
	tree := git(nil, "mktree")
	local.base = git([]byte("Original\n"), "commit-tree", tree)
	git(nil, "push", "--quiet", "origin", local.base+":refs/heads/main")
	commit := git([]byte("Review\n"), "commit-tree", tree, "-p", local.base)
	return local, remote, Draft{commit: commit}
}

func TestPublishWaitsForGitLabSourceValidation(t *testing.T) {
	var calls atomic.Int32
	r, _, draft := publishFixture(t, "gitlab", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" || !strings.HasSuffix(request.URL.Path, "/merge_requests") {
			t.Error("unexpected API request", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, missingGitLabSource)
			return
		}
		fmt.Fprint(w, `{"iid":7,"web_url":"https://gitlab.example.test/fixture/repo/-/merge_requests/7"}`)
	})
	result, err := r.Publish(t.Context(), draft, "fixture/update")
	if err != nil || result.Review.Number != 7 || calls.Load() != 3 {
		t.Fatal("review did not recover from explicit source validation", result, err, calls.Load())
	}
	if head, err := r.remoteHead(t.Context(), "fixture/update"); err != nil || head != draft.commit {
		t.Fatal("published branch changed", head, err)
	}
	if head, err := r.remoteHead(t.Context(), "main"); err != nil || head != r.base {
		t.Fatal("base changed", head, err)
	}
}

func TestPublishStopsWhenGitLabRefsChange(t *testing.T) {
	for _, branch := range []string{"main", "fixture/update"} {
		t.Run(branch, func(t *testing.T) {
			var calls atomic.Int32
			var remote *Repository
			var changed string
			r, repository, draft := publishFixture(t, "gitlab", func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if _, err := remote.git(request.Context(), nil, "update-ref", "refs/heads/"+branch, changed); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, missingGitLabSource)
			})
			remote = repository
			changed = r.base
			if branch == "main" {
				changed = draft.commit
			}
			result, err := r.Publish(t.Context(), draft, "fixture/update")
			if !errors.Is(err, ErrConflict) || calls.Load() != 1 || result.Review.Number != 0 || result.Commit != draft.commit {
				t.Fatal("changed ref permitted another creation request", result, err, calls.Load())
			}
		})
	}
}

func TestPublishDoesNotRetryAmbiguousOrOtherRejections(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		status           int
		abort            bool
	}{
		{name: "transport", kind: "gitlab", abort: true},
		{name: "server failure", kind: "gitlab", status: 500, body: missingGitLabSource},
		{name: "other validation", kind: "gitlab", status: 400, body: `{"message":{"target_branch":["does not exist"]}}`},
		{name: "extra validation", kind: "gitlab", status: 400, body: `{"message":{"source_branch":["does not exist"],"title":["is invalid"]}}`},
		{name: "malformed", kind: "gitlab", status: 400, body: missingGitLabSource + " trailing"},
		{name: "other provider", kind: "gitea", status: 400, body: missingGitLabSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			r, _, draft := publishFixture(t, tc.kind, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.abort {
					panic(http.ErrAbortHandler)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			result, err := r.Publish(t.Context(), draft, "fixture/update")
			if err == nil || calls.Load() != 1 || result.Review.Number != 0 || result.Commit != draft.commit {
				t.Fatal("ambiguous or unrelated rejection was retried", result, err, calls.Load())
			}
			if strings.Contains(err.Error(), "synthetic-token") || strings.Contains(err.Error(), tc.body) && tc.body != "" {
				t.Fatal("remote response leaked into error", err)
			}
			if head, err := r.remoteHead(t.Context(), "fixture/update"); err != nil || head != draft.commit {
				t.Fatal("failure lost the published branch", head, err)
			}
		})
	}
}

func TestPublishGitLabValidationWaitHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	r, _, draft := publishFixture(t, "gitlab", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, missingGitLabSource)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := r.Publish(ctx, draft, "fixture/update")
	if err == nil || ctx.Err() == nil || time.Since(started) > 2*time.Second || result.Review.Number != 0 {
		t.Fatal("validation wait ignored caller deadline", result, err, calls.Load())
	}
}
