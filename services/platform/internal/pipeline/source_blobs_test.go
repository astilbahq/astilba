package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestGitBlobIDMatchesGit(t *testing.T) {
	sha1Commit, sha256Commit := strings.Repeat("a", 40), strings.Repeat("a", 64)
	for _, want := range []struct{ commit, content, id string }{
		{sha1Commit, "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{sha1Commit, "hello\n", "ce013625030ba8dba906f756967f9e9ca394464a"},
		{sha1Commit, "Hello World!\n", "980a0d5f19a64b4b30a87d4206aade58726b60e3"},
		{sha256Commit, "", "473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813"},
		{sha256Commit, "hello\n", "2cf8d83d9ee29543b34a87727421fdecb7e3f3a183d337639025de576db9ebb4"},
	} {
		if got := GitBlobID(want.commit, want.content); got != want.id {
			t.Fatalf("%q in a %d-character repository: %s want %s", want.content, len(want.commit), got, want.id)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	// Multi-byte and CRLF contents, which any text handling on either side would change.
	for _, content := range []string{"crlf\r\nline\r\n", "ünïcödé — ✓\n", "no newline"} {
		command := exec.Command("git", "hash-object", "--stdin")
		command.Stdin = strings.NewReader(content)
		out, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := GitBlobID(sha1Commit, content); got != strings.TrimSpace(string(out)) {
			t.Fatalf("%q: %s want %s", content, got, out)
		}
	}
}

// Response bodies follow what github.com, gitlab.com and a real Gitea returned for the same
// kinds of entry, including what each said about symlinks, submodules and directories.
func TestSourceBlobsThroughFileAPIs(t *testing.T) {
	commit, blob := strings.Repeat("c", 40), strings.Repeat("b", 40)
	file := func(path string) string {
		return `{"type":"file","path":"` + path + `","sha":"` + blob + `","encoding":"base64","content":"c2VydmljZXM6Cg=="}`
	}
	gitlabFile := func(path, at string) string {
		return `{"file_path":"` + path + `","blob_id":"` + blob + `","commit_id":"` + at + `","ref":"` + commit + `","encoding":"base64","content":"c2VydmljZXM6Cg=="}`
	}
	for _, tc := range []struct {
		kind, endpoint string
		answers        map[string]string
	}{
		{"github", "/repos/team/app/contents/", map[string]string{
			"compose.yaml":       file("compose.yaml"),
			"deep/dir/a b+c.yml": file("deep/dir/a b+c.yml"),
			"followed":           file("elsewhere/target.yaml"),
			"directory":          `[{"type":"file","path":"directory/a","sha":"` + blob + `"}]`,
		}},
		{"gitlab", "/api/v4/projects/team%2Fapp/repository/files/", map[string]string{
			"compose.yaml":       gitlabFile("compose.yaml", commit),
			"deep/dir/a b+c.yml": gitlabFile("deep/dir/a b+c.yml", commit),
			"followed":           gitlabFile("elsewhere/target.yaml", commit),
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var asked []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.URL.EscapedPath())
				if r.URL.Query().Get("ref") != commit {
					t.Error("not asked at the exact commit", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.EscapedPath(), "oversized") {
					// A response past the client's limit, and a runner refuses such a file anyway.
					_, _ = w.Write([]byte(`{"content":"` + strings.Repeat("a", 1<<20) + `"}`))
					return
				}
				for name, body := range tc.answers {
					escaped := strings.ReplaceAll(name, " ", "%20")
					if tc.kind == "gitlab" {
						escaped = strings.ReplaceAll(strings.ReplaceAll(escaped, "/", "%2F"), "+", "%2B")
					}
					if r.URL.EscapedPath() == tc.endpoint+escaped {
						_, _ = w.Write([]byte(body))
						return
					}
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			connection := Connection{Provider: tc.kind, BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}
			policy := Policy{Development: true}
			if tc.kind == "github" {
				connection.BaseURL = "https://api.github.com"
				policy.GitHubFixture = server.URL
			}
			p, err := NewProvider(connection, Credential{Password: "fixture-token"}, policy)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{"compose.yaml", "deep/dir/a b+c.yml", "followed", "directory", "oversized", "missing"}
			blobs, err := p.SourceBlobs(context.Background(), commit, paths)
			if err != nil {
				t.Fatal(err)
			}
			if len(blobs) != 2 || blobs["compose.yaml"] != blob || blobs["deep/dir/a b+c.yml"] != blob {
				t.Fatalf("only entries at their own path have an answer: %v", blobs)
			}
			if len(asked) != len(paths) {
				t.Fatalf("asked %d times for %d paths: %v", len(asked), len(paths), asked)
			}
		})
	}
}

// GitLab says which commit it read, so a ref named after the commit cannot pass for it.
func TestSourceBlobsRefusesGitLabAnswerAboutAnotherCommit(t *testing.T) {
	commit, blob := strings.Repeat("c", 40), strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"file_path":"a","blob_id":"` + blob + `","commit_id":"` + strings.Repeat("d", 40) + `"}`))
	}))
	defer server.Close()
	p, err := NewProvider(Connection{Provider: "gitlab", BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}, Credential{}, Policy{Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if blobs, err := p.SourceBlobs(context.Background(), commit, []string{"a"}); err == nil {
		t.Fatal("trusted an answer about another commit", blobs)
	}
}

// A Gitea ref can be named after a commit, and its contents API then answers about the ref, so
// only the commit's own trees may be asked. Bodies follow a real Gitea's.
func TestSourceBlobsWalksGiteaTrees(t *testing.T) {
	commit, blob, sub, deeper := strings.Repeat("c", 40), strings.Repeat("b", 40), strings.Repeat("e", 40), strings.Repeat("f", 40)
	entry := func(path, mode, kind, sha string) string {
		return `{"path":"` + path + `","mode":"` + mode + `","type":"` + kind + `","size":1,"sha":"` + sha + `","url":"x"}`
	}
	tree := func(total int, entries ...string) string {
		return `{"sha":"x","url":"x","tree":[` + strings.Join(entries, ",") + `],"truncated":false,"page":1,"total_count":` + strconv.Itoa(total) + `}`
	}
	asked := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + "?" + r.URL.Query().Get("page")
		asked[key]++
		switch key {
		case "/api/v1/repos/team/app/git/trees/" + commit + "?1":
			_, _ = w.Write([]byte(tree(4, entry("compose.yaml", "100644", "blob", blob), entry("deep", "040000", "tree", sub))))
		case "/api/v1/repos/team/app/git/trees/" + commit + "?2":
			_, _ = w.Write([]byte(tree(4, entry("second-page.yaml", "100644", "blob", blob), entry("module", "160000", "commit", blob))))
		case "/api/v1/repos/team/app/git/trees/" + sub + "?1":
			_, _ = w.Write([]byte(tree(2, entry("dir", "040000", "tree", deeper), entry("run.sh", "100755", "blob", blob))))
		case "/api/v1/repos/team/app/git/trees/" + deeper + "?1":
			_, _ = w.Write([]byte(tree(1, entry("a b.yml", "100644", "blob", blob))))
		default:
			t.Error("asked about something other than the commit's trees", key)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	p, err := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}, Credential{}, Policy{Development: true})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := p.SourceBlobs(context.Background(), commit, []string{"compose.yaml", "deep/run.sh", "deep/dir/a b.yml", "second-page.yaml", "compose.yaml/x", "module/x", "missing", "deep/missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 4 || blobs["compose.yaml"] != blob || blobs["deep/run.sh"] != blob || blobs["deep/dir/a b.yml"] != blob || blobs["second-page.yaml"] != blob {
		t.Fatalf("only entries that exist have an answer: %v", blobs)
	}
	for key, count := range asked {
		if count != 1 {
			t.Fatalf("read %s %d times", key, count)
		}
	}
}

// A Gitea server decides what its listing says, and must not decide how long management reads.
func TestSourceBlobsBoundsAGiteaWalk(t *testing.T) {
	commit := strings.Repeat("c", 40)
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"tree":[{"path":"entry-` + strconv.Itoa(asked) + `","type":"blob","sha":"` + strings.Repeat("b", 40) + `"}],"total_count":1000000}`))
	}))
	defer server.Close()
	p, err := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}, Credential{}, Policy{Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if blobs, err := p.SourceBlobs(context.Background(), commit, []string{"a"}); err == nil {
		t.Fatal("an endless directory gave an answer", blobs)
	}
	if asked != treeWalkPages {
		t.Fatalf("read %d pages", asked)
	}
}

// A failed answer stops the asking, and is never mistaken for the path being absent.
func TestSourceBlobsFailures(t *testing.T) {
	commit := strings.Repeat("c", 40)
	for name, tc := range map[string]struct {
		kind   string
		status int
		body   string
		commit string
	}{
		"unavailable":      {kind: "github", status: 503, commit: commit},
		"rate limited":     {kind: "gitlab", status: 429, commit: commit},
		"forbidden":        {kind: "gitea", status: 403, commit: commit},
		"gitea bad ref":    {kind: "gitea", status: 400, commit: commit},
		"cut short":        {kind: "github", status: 200, body: "truncate", commit: commit},
		"cut short tree":   {kind: "gitea", status: 200, body: "truncate", commit: commit},
		"not json":         {kind: "github", status: 200, body: "<html>", commit: commit},
		"empty blob":       {kind: "github", status: 200, body: `{"type":"file","path":"a","sha":""}`, commit: commit},
		"no blob":          {kind: "gitlab", status: 200, body: `{"file_path":"a","commit_id":"` + commit + `"}`, commit: commit},
		"short blob":       {kind: "github", status: 200, body: `{"type":"file","path":"a","sha":"abc"}`, commit: commit},
		"uppercase blob":   {kind: "github", status: 200, body: `{"type":"file","path":"a","sha":"` + strings.Repeat("B", 40) + `"}`, commit: commit},
		"zero blob":        {kind: "github", status: 200, body: `{"type":"file","path":"a","sha":"` + strings.Repeat("0", 40) + `"}`, commit: commit},
		"other hash width": {kind: "github", status: 200, body: `{"type":"file","path":"a","sha":"` + strings.Repeat("b", 40) + `"}`, commit: strings.Repeat("c", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			asked := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked++
				if tc.body == "truncate" {
					// Promise more than is sent, so reading the body fails after a 200.
					w.Header().Set("Content-Length", "100")
					w.WriteHeader(200)
					_, _ = w.Write([]byte("{"))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			connection := Connection{Provider: tc.kind, BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}
			policy := Policy{Development: true}
			if tc.kind == "github" {
				connection.BaseURL = "https://api.github.com"
				policy.GitHubFixture = server.URL
			}
			p, err := NewProvider(connection, Credential{}, policy)
			if err != nil {
				t.Fatal(err)
			}
			blobs, err := p.SourceBlobs(context.Background(), tc.commit, []string{"a", "b"})
			if err == nil || blobs != nil {
				t.Fatalf("no failure from %v: %v", tc, blobs)
			}
			if asked != 1 {
				t.Fatalf("kept asking after a failure: %d", asked)
			}
			var status *ProviderError
			if tc.status != 200 && (!errors.As(err, &status) || status.Status != tc.status) {
				t.Fatalf("the status was lost: %v", err)
			}
		})
	}
}

func TestSourceBlobsInASHA256Repository(t *testing.T) {
	commit, blob := strings.Repeat("c", 64), strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tree":[{"path":"a","mode":"100644","type":"blob","sha":"` + blob + `"}],"total_count":1}`))
	}))
	defer server.Close()
	p, err := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app", RepositoryID: 12}, Credential{}, Policy{Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if blobs, err := p.SourceBlobs(context.Background(), commit, []string{"a"}); err != nil || blobs["a"] != blob {
		t.Fatal(blobs, err)
	}
}

// An agent repeats the digest to accept a command, with its own idea of a command's fields.
func TestCommandDigestIgnoresSourceBlobs(t *testing.T) {
	plain := Command{Action: "import", Commit: strings.Repeat("c", 40)}
	frozen := plain
	frozen.SourceBlobs = map[string]string{"a": strings.Repeat("b", 40)}
	if plain.Digest() != frozen.Digest() {
		t.Fatal("source blobs changed the digest an older agent would repeat")
	}
}
