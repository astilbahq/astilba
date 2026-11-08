package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// gitTree is what a provider's repository holds at each commit. It answers about files as
// GitHub, GitLab and Gitea do, and has no opinion about what any runner reports.
type gitTree struct {
	mu      sync.Mutex
	commits map[string]map[string]string
	// directories holds each commit's directory by the object name its parent gives it.
	directories map[string]gitDirectory
}

type gitDirectory struct{ commit, path string }

func directoryID(commit, path string) string {
	sum := sha256.Sum256([]byte(commit + "/" + path))
	return hex.EncodeToString(sum[:20])
}

func (g *gitTree) record(commit string, files []composeproject.File) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.commits == nil {
		g.commits, g.directories = map[string]map[string]string{}, map[string]gitDirectory{}
	}
	tree := map[string]string{}
	for _, file := range files {
		tree[file.Path] = file.Content
	}
	g.commits[commit] = tree
	g.directories[commit] = gitDirectory{commit, ""}
}

// listing is a Gitea tree: the entries directly inside a directory of a commit.
func (g *gitTree) listing(id string) ([]map[string]string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	directory, known := g.directories[id]
	if !known {
		return nil, false
	}
	entries := map[string]map[string]string{}
	for name, content := range g.commits[directory.commit] {
		inside, found := strings.CutPrefix(name, directory.path)
		if !found {
			continue
		}
		if first, _, deeper := strings.Cut(inside, "/"); deeper {
			sub := directoryID(directory.commit, directory.path+first+"/")
			g.directories[sub] = gitDirectory{directory.commit, directory.path + first + "/"}
			entries[first] = map[string]string{"path": first, "type": "tree", "sha": sub}
		} else {
			entries[inside] = map[string]string{"path": inside, "type": "blob", "sha": pipeline.GitBlobID(directory.commit, content)}
		}
	}
	out := []map[string]string{}
	for _, entry := range entries {
		out = append(out, entry)
	}
	return out, true
}

// fileQuestion recognises a request about a repository's files, with the path it names.
func fileQuestion(r *http.Request) (string, bool) {
	if _, name, found := strings.Cut(r.URL.Path, "/contents/"); found {
		return name, true
	}
	if _, name, found := strings.Cut(r.URL.Path, "/repository/files/"); found {
		return name, true
	}
	return "", strings.Contains(r.URL.Path, "/git/trees/")
}

// answer serves a request about files, and reports whether this was one.
func (g *gitTree) answer(w http.ResponseWriter, r *http.Request) bool {
	name, found := fileQuestion(r)
	if !found {
		return false
	}
	if _, id, trees := strings.Cut(r.URL.Path, "/git/trees/"); trees {
		entries, known := g.listing(id)
		if !known {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		_ = stablejson.Encode(w, map[string]any{"sha": id, "tree": entries, "truncated": false, "page": 1, "total_count": len(entries)})
		return true
	}
	commit := r.URL.Query().Get("ref")
	g.mu.Lock()
	content, found := g.commits[commit][name]
	g.mu.Unlock()
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	blob := pipeline.GitBlobID(commit, content)
	if strings.Contains(r.URL.Path, "/repository/files/") {
		_ = stablejson.Encode(w, map[string]any{"file_path": name, "blob_id": blob, "commit_id": commit, "ref": commit, "encoding": "base64", "content": ""})
		return true
	}
	_ = stablejson.Encode(w, map[string]any{"type": "file", "path": name, "sha": blob, "encoding": "base64", "content": ""})
	return true
}

// gitSource is a provider of one kind whose branch head the test moves, with signed webhook
// delivery. Questions about files can be held or made to fail.
type gitSource struct {
	t          *testing.T
	f          *fixture
	kind       string
	tree       gitTree
	head       atomic.Value
	connection pipeline.Connection
	secret     string
	// outage answers questions about files with this status while it is set.
	outage atomic.Int64
	// failing limits outage to questions about this path, when set.
	failing atomic.Value
	asked   atomic.Int64

	mu      sync.Mutex
	gate    chan struct{}
	entered chan struct{}
	holding string
}

func newGitSource(t *testing.T, f *fixture, b *browser, prefix string) *gitSource {
	t.Helper()
	return newGitSourceOf(t, f, b, prefix, "gitea")
}

func newGitSourceOf(t *testing.T, f *fixture, b *browser, prefix, kind string) *gitSource {
	t.Helper()
	g := &gitSource{t: t, f: f, kind: kind}
	g.head.Store(strings.Repeat("a", 40))
	g.failing.Store("")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if name, file := fileQuestion(r); file {
			g.asked.Add(1)
			g.mu.Lock()
			gate, entered, holding := g.gate, g.entered, g.holding
			g.mu.Unlock()
			if gate != nil && (holding == "" || holding == name) {
				entered <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			failing := g.failing.Load().(string)
			if status := g.outage.Load(); status != 0 && (failing == "" || failing == name) {
				w.WriteHeader(int(status))
				return
			}
			g.tree.answer(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/branches/") {
			answerBranch(w, kind, g.head.Load().(string))
			return
		}
		answerRepository(w, kind, 12, false)
	}))
	t.Cleanup(provider.Close)
	base := provider.URL
	if kind == "github" {
		base = "https://api.github.com"
		f.execution.Policy.GitHubFixture = provider.URL
	}
	var created struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Source", "provider": kind, "baseUrl": base, "repository": "team/app"}, 201, &created)
	g.connection, g.secret = created.Connection, created.WebhookSecret
	return g
}

// hold makes questions about files wait until the returned release runs.
func (g *gitSource) hold(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	return g.holdPath(t, "")
}

// holdPath is hold for questions about one path, when it names one.
func (g *gitSource) holdPath(t *testing.T, path string) (<-chan struct{}, func()) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 32)
	g.mu.Lock()
	g.gate, g.entered, g.holding = gate, entered, path
	g.mu.Unlock()
	release := sync.OnceFunc(func() {
		g.mu.Lock()
		g.gate = nil
		g.mu.Unlock()
		close(gate)
	})
	t.Cleanup(release)
	return entered, release
}

// push moves the branch and delivers the webhook that provider would send.
func (g *gitSource) push(commit string) {
	g.t.Helper()
	g.head.Store(commit)
	body := map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}}
	if g.kind == "gitlab" {
		body["project"] = map[string]int{"id": 12}
	}
	raw, _ := stablejson.Marshal(body)
	r, _ := http.NewRequest("POST", g.f.server.URL+"/hooks/git/"+g.connection.ID.String(), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write(raw)
	switch g.kind {
	case "github":
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-GitHub-Delivery", uuid.NewString())
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	case "gitlab":
		r.Header.Set("X-Gitlab-Event", "Push Hook")
		r.Header.Set("webhook-id", uuid.NewString())
		r.Header.Set("X-Gitlab-Token", g.secret)
	default:
		r.Header.Set("X-Gitea-Event", "push")
		r.Header.Set("X-Gitea-Delivery", uuid.NewString())
		r.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	res, err := g.f.server.Client().Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		g.t.Fatal("webhook rejected", res.StatusCode)
	}
}

// reportImport reports a runner's success with these files.
func (a authorityAgent) reportImport(c api.ExecutionCommand, commit string, files []composeproject.File) {
	a.t.Helper()
	raw, _ := stablejson.Marshal(pipeline.Result{Commit: commit, Files: files})
	message := jsontext.Value(raw)
	agentRequest(a.t, a.f.server.Client(), a.f.server.URL, "/agent/v1/report", a.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &message}, 204, nil)
}
