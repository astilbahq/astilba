package pipeline

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// These answers were recorded from real providers on 2026-11-08 and cut down to the fields
// around the ones read: Gitea 28.2.0, api.github.com (octocat/Hello-World, cli/cli) and
// gitlab.com (gitlab-org/gitlab-test, inkscape/inkscape).
const (
	giteaBranch  = `{"name":"main","commit":{"id":"068212663d27615e0d7d8e32d425edb1d85c1bac","message":"one\n","url":"http://127.0.0.1:13000/owner/app/commit/068212663d27615e0d7d8e32d425edb1d85c1bac","timestamp":"2026-11-08T13:47:47Z","added":null,"removed":null,"modified":null},"protected":false,"user_can_push":true,"user_can_merge":true}`
	githubBranch = `{"name":"master","commit":{"sha":"7fd1a60b01f91b314f59955a4e4d4e80d8edf11d","node_id":"MDY6Q29tbWl0MTI5NjI2OTo3ZmQxYTYwYjAxZjkxYjMxNGY1OTk1NWE0ZTRkNGU4MGQ4ZWRmMTFk","url":"https://api.github.com/repos/octocat/Hello-World/commits/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"},"protected":false}`
	gitlabBranch = `{"name":"master","commit":{"id":"ddd0f15ae83993f5cb66a927a28673882e99100b","short_id":"ddd0f15a","title":"Merge branch 'master' into 'master'"},"merged":false,"protected":true,"default":true}`
)

func answering(t *testing.T, kind string, answer http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(answer)
	t.Cleanup(server.Close)
	origin, policy := server.URL, Policy{Development: true}
	if kind == "github" {
		origin, policy.GitHubFixture = "https://api.github.com", server.URL
	}
	p, err := NewProvider(Connection{Provider: kind, BaseURL: origin, Repository: "owner/app", RepositoryID: 1, Private: true}, Credential{Password: "token"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A real Gitea branch names its commit id, never sha, so reading sha left every Gitea
// connection unable to start an import or a build.
func TestResolveReadsEachProvidersBranchAnswer(t *testing.T) {
	for kind, answer := range map[string]struct{ branch, commit string }{
		"gitea":  {giteaBranch, "068212663d27615e0d7d8e32d425edb1d85c1bac"},
		"github": {githubBranch, "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"},
		"gitlab": {gitlabBranch, "ddd0f15ae83993f5cb66a927a28673882e99100b"},
	} {
		t.Run(kind, func(t *testing.T) {
			p := answering(t, kind, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					_, _ = w.Write([]byte(answer.branch))
					return
				}
				_, _ = w.Write([]byte(`{"id":1,"private":true,"visibility":"private","owner":{"visibility":"public"}}`))
			})
			if got, err := p.Resolve(t.Context(), "main"); err != nil || got != answer.commit {
				t.Fatalf("resolved %q, %v", got, err)
			}
		})
	}
}

// Gitea answers private: false for a repository in an organisation only its members or
// signed-in users can see, which nobody can clone anonymously.
func TestGiteaRepositoryIsPrivateUnlessItsOwnerIsPublic(t *testing.T) {
	for visibility, private := range map[string]bool{"public": false, "limited": true, "private": true} {
		p := answering(t, "gitea", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":1,"owner":{"id":2,"login":"team","visibility":"` + visibility + `"},"name":"lib","full_name":"team/lib","private":false,"internal":` + strconv.FormatBool(visibility == "private") + `}`))
		})
		if _, got, err := p.Repository(t.Context()); err != nil || got != private {
			t.Fatalf("owner %s: private=%t, %v", visibility, got, err)
		}
	}
}

// A GitLab project that merges by fast-forward answers merge_commit_sha: null.
func TestMergedCommitFollowsEachMergeMethod(t *testing.T) {
	head, merge, squash := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	cases := []struct {
		name, kind, answer, want string
	}{
		{"gitlab merge commit", "gitlab", `{"iid":7,"state":"merged","merge_commit_sha":"` + merge + `","squash_commit_sha":null,"sha":"` + head + `"}`, merge},
		{"gitlab fast-forward", "gitlab", `{"iid":7,"state":"merged","merge_commit_sha":null,"squash_commit_sha":null,"sha":"` + head + `","squash":false}`, head},
		{"gitlab squashed fast-forward", "gitlab", `{"iid":7,"state":"merged","merge_commit_sha":null,"squash_commit_sha":"` + squash + `","sha":"` + head + `","squash":true}`, squash},
		{"gitlab open", "gitlab", `{"iid":7,"state":"opened","merge_commit_sha":null,"sha":"` + head + `"}`, ""},
		{"github merged", "github", `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"` + merge + `"}`, merge},
		// GitHub names a test merge commit for an open request.
		{"github open", "github", `{"number":7,"state":"open","merged":false,"merge_commit_sha":"` + merge + `"}`, ""},
		{"gitea fast-forward-only", "gitea", `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"` + head + `","head":{"sha":"` + head + `"}}`, head},
		{"gitea closed", "gitea", `{"number":7,"state":"closed","merged":false,"merge_commit_sha":null}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := answering(t, c.kind, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/7") {
					_, _ = w.Write([]byte(c.answer))
					return
				}
				_, _ = w.Write([]byte(`{"id":1,"private":true,"visibility":"private","owner":{"visibility":"public"}}`))
			})
			if got, err := p.MergedCommit(t.Context(), 7); err != nil || got != c.want {
				t.Fatalf("merged as %q, %v", got, err)
			}
		})
	}
}

type pull struct {
	number           int
	base, head       string
	headRepo         int64
	targetRepository int64
	closed           bool
}

func (p pull) answer(kind string) map[string]any {
	state := "open"
	if kind == "gitlab" {
		state = "opened"
	}
	if p.closed {
		state = "closed"
	}
	link := "https://example.test/" + strconv.Itoa(p.number)
	return map[string]any{"number": p.number, "iid": p.number, "html_url": link, "web_url": link, "state": state,
		"head": map[string]any{"ref": p.head, "repo": map[string]any{"id": p.headRepo}, "repo_id": p.headRepo}, "base": map[string]any{"ref": p.base, "repo": map[string]any{"id": p.targetRepository}},
		"source_branch": p.head, "target_branch": p.base, "source_project_id": p.headRepo, "target_project_id": p.targetRepository}
}

// pulls answers requests about pull requests as each provider does, numbered in the order they
// were opened. GitLab's list is newest first unless sort=asc. Gitea's list filters only by base_branch, pages newest first unless
// sort=oldest, and gives at most its page cap however many the limit asks for; its lookup by
// base and head names the oldest such request from this repository, closed or not, and it
// refuses a second open one. GitLab's source_branch filter keeps fork requests, while
// GitHub's head names the fork's owner.
type pulls struct {
	kind    string
	cap     int
	list    []pull
	created []int
}

func (s *pulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, lookup, isLookup := strings.Cut(r.URL.RawPath, "/pulls/")
	switch {
	case r.Method == "POST":
		var asked struct {
			Base string `json:"base"`
			Head string `json:"head"`
		}
		_ = json.UnmarshalRead(r.Body, &asked)
		for _, p := range s.list {
			if s.kind == "gitea" && !p.closed && p.headRepo == 1 && p.base == asked.Base && p.head == asked.Head {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		n := len(s.list) + 1
		s.created = append(s.created, n)
		s.list = append(s.list, pull{number: n, base: asked.Base, head: asked.Head, headRepo: 1, targetRepository: 1})
		_ = stablejson.Encode(w, s.list[n-1].answer(s.kind))
		return
	case s.kind == "gitea" && isLookup:
		base, head, _ := strings.Cut(lookup, "/")
		base, _ = url.PathUnescape(base)
		head, _ = url.PathUnescape(head)
		for _, p := range s.list {
			if p.headRepo == 1 && p.base == base && p.head == head {
				_ = stablejson.Encode(w, p.answer(s.kind))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rows := []pull{}
	for _, p := range s.list {
		if p.closed {
			continue
		}
		switch s.kind {
		case "gitea":
			if b := q.Get("base_branch"); b != "" && p.base != b {
				continue
			}
		case "gitlab":
			if p.head != q.Get("source_branch") && q.Get("source_branch") != "" || p.base != q.Get("target_branch") && q.Get("target_branch") != "" {
				continue
			}
		case "github":
			if q.Get("head") != "" && (q.Get("head") != "owner:"+p.head || p.headRepo != 1) || q.Get("base") != "" && p.base != q.Get("base") {
				continue
			}
		}
		rows = append(rows, p)
	}
	size, _ := strconv.Atoi(q.Get("per_page"))
	defaults := map[string]int{"github": 30, "gitlab": 20, "gitea": 30}
	if s.kind == "gitlab" && q.Get("sort") != "asc" {
		slices.Reverse(rows)
	}
	if s.kind == "gitea" {
		if q.Get("sort") != "oldest" {
			slices.Reverse(rows)
		}
		size, _ = strconv.Atoi(q.Get("limit"))
		if s.cap > 0 {
			size = min(size, s.cap)
		}
	}
	if size < 1 {
		size = defaults[s.kind]
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	out := []map[string]any{}
	for _, p := range rows[min(len(rows), (page-1)*size):min(len(rows), page*size)] {
		out = append(out, p.answer(s.kind))
	}
	_ = stablejson.Encode(w, out)
}

func reviewing(t *testing.T, kind string, list []pull) (*Provider, *pulls) {
	t.Helper()
	s := &pulls{kind: kind, list: list}
	return answering(t, kind, s.ServeHTTP), s
}

func numbered(list []pull) []pull {
	for i := range list {
		list[i].number = i + 1
	}
	return list
}

// A fork can open a request from a branch named like a submission's. Recording it would show
// the owner someone else's change as their own review.
func TestReviewIgnoresAForksRequestFromTheSameBranchName(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			p, s := reviewing(t, kind, numbered([]pull{{base: "main", head: "astilba/change", headRepo: 99, targetRepository: 1}}))
			got, err := p.Review(context.Background(), "main", "astilba/change")
			if err != nil || got.Number != 2 || len(s.created) != 1 {
				t.Fatalf("review %+v, created %v, %v", got, s.created, err)
			}
		})
	}
}

// A retry finds the review an earlier attempt opened, however many requests came after it.
// Gitea's list ignores head, so missing it means asking for a duplicate, which Gitea refuses.
func TestReviewFindsItsRequestAmongManyOthers(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			list := []pull{{base: "main", head: "astilba/change", headRepo: 1, targetRepository: 1}}
			for n := 2; n <= 120; n++ {
				list = append(list, pull{base: []string{"main", "other"}[n%2], head: "feature-" + strconv.Itoa(n), headRepo: 1, targetRepository: 1})
			}
			p, s := reviewing(t, kind, numbered(list))
			got, err := p.Review(context.Background(), "main", "astilba/change")
			if err != nil || got.Number != 1 || len(s.created) != 0 {
				t.Fatalf("review %+v, created %v, %v", got, s.created, err)
			}
		})
	}
}

// On GitLab, forks can open requests from a branch named like the submission's once it is
// pushed, and the list keeps them. However many there are, a retry finds its own review.
func TestGitLabReviewIsFoundAmongForksRequestsFromItsBranch(t *testing.T) {
	list := []pull{{base: "main", head: "astilba/change", headRepo: 1, targetRepository: 1}}
	for n := range 25 {
		list = append(list, pull{base: "main", head: "astilba/change", headRepo: int64(100 + n), targetRepository: 1})
	}
	p, s := reviewing(t, "gitlab", numbered(list))
	got, err := p.Review(context.Background(), "main", "astilba/change")
	if err != nil || got.Number != 1 || len(s.created) != 0 {
		t.Fatalf("review %+v, created %v, %v", got, s.created, err)
	}
}

// Anyone who can read a Gitea repository can open requests into it from a fork. However many
// there are, they must not stop a submission opening its review.
func TestGiteaReviewIsNotHeldUpByOtherRequests(t *testing.T) {
	list := []pull{}
	for n := range 1100 {
		list = append(list, pull{base: "main", head: "spam-" + strconv.Itoa(n), headRepo: 99, targetRepository: 1})
	}
	p, s := reviewing(t, "gitea", numbered(list))
	got, err := p.Review(context.Background(), "main", "astilba/change")
	if err != nil || got.Number != 1101 || len(s.created) != 1 {
		t.Fatalf("review %+v, created %v, %v", got, s.created, err)
	}
}

// Gitea's lookup names the oldest request from the branch, which can be a closed one, while
// the review a retry needs is open further down a list whose pages a server can cap below
// what the limit asks for.
func TestGiteaReviewFindsTheOpenRequestBehindAClosedOne(t *testing.T) {
	list := []pull{{base: "main", head: "astilba/change", headRepo: 1, targetRepository: 1, closed: true}, {base: "main", head: "astilba/change", headRepo: 1, targetRepository: 1}}
	for n := range 40 {
		list = append(list, pull{base: "main", head: "feature-" + strconv.Itoa(n), headRepo: 1, targetRepository: 1})
	}
	p, s := reviewing(t, "gitea", numbered(list))
	s.cap = 30
	got, err := p.Review(context.Background(), "main", "astilba/change")
	if err != nil || got.Number != 2 || len(s.created) != 0 {
		t.Fatalf("review %+v, created %v, %v", got, s.created, err)
	}
}

// Gitea ignores base and the ordering GitHub and GitLab are asked for. Discovery then pages
// through every base's requests newest first, so a request opened while it pages moves the
// others onto pages it has already read.
func TestGiteaDiscoveryPagesTheBranchsRequestsOldestFirst(t *testing.T) {
	list := []pull{}
	for n := 1; n <= 120; n++ {
		base := "main"
		if n%3 == 0 {
			base = "other"
		}
		list = append(list, pull{number: n, base: base, head: "feature-" + strconv.Itoa(n), headRepo: 1, targetRepository: 1})
	}
	answers := &pulls{kind: "gitea", list: list}
	p := answering(t, "gitea", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			answers.ServeHTTP(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"private":true,"owner":{"visibility":"public"}}`))
	})
	first, err := p.OpenPullRequests(t.Context(), "main", 1)
	if err != nil || len(first) != 50 || first[0] != 1 || first[1] != 2 || first[2] != 4 {
		t.Fatalf("first page %v, %v", first, err)
	}
}
