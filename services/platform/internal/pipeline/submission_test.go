package pipeline

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// forgeFile is a file in a fake repository's commit.
type forgeFile struct{ Content, Mode string }

type forgeCommit struct {
	Parents []string
	Message string
	Files   map[string]forgeFile
}

// forge is an in-memory repository answering the parts of GitHub's, GitLab's and Gitea's APIs
// that submissions use, as their documentation and a real Gitea describe them. It enforces what
// the submission relies on: create-only branches, a commit's own parents and per-file blob guards.
type forge struct {
	kind    string
	mu      sync.Mutex
	commits map[string]forgeCommit
	// dirs names each directory of a commit a client may ask for, and built each tree a
	// GitHub client made.
	dirs     map[string][2]string
	built    map[string]map[string]forgeFile
	branches map[string]string
	reviews  map[string]int
	writes   []string
	// down answers every request with this status while it is set.
	down int
}

const (
	forgeWriter = "writer-token"
	forgeReader = "reader-token"
)

func newForge(kind string, files map[string]forgeFile) (*forge, string) {
	f := &forge{kind: kind, commits: map[string]forgeCommit{}, dirs: map[string][2]string{}, built: map[string]map[string]forgeFile{}, branches: map[string]string{}, reviews: map[string]int{}}
	base := f.commit(forgeCommit{Message: "base", Files: files})
	f.branches["main"] = base
	return f, base
}

func objectID(parts ...any) string {
	raw, _ := stablejson.Marshal(parts)
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}

func (f *forge) commit(c forgeCommit) string {
	id := objectID("commit", c)
	f.commits[id] = c
	return id
}

// treeID names the directory dir of a commit, as an object name a client can ask for.
func (f *forge) treeID(commit, dir string) string {
	id := objectID("tree", commit, dir)
	f.dirs[id] = [2]string{commit, dir}
	return id
}

func (f *forge) entries(id string) ([]map[string]string, bool) {
	commit, dir := id, ""
	if _, ok := f.commits[id]; !ok {
		named, ok := f.dirs[id]
		if !ok {
			return nil, false
		}
		commit, dir = named[0], named[1]
	}
	seen := map[string]bool{}
	out := []map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(f.commits[commit].Files)) {
		rest, inside := strings.CutPrefix(name, dir)
		if dir != "" && !inside {
			continue
		}
		head, _, nested := strings.Cut(rest, "/")
		if seen[head] {
			continue
		}
		seen[head] = true
		if nested {
			out = append(out, map[string]string{"path": head, "type": "tree", "mode": "040000", "sha": f.treeID(commit, dir+head+"/")})
			continue
		}
		file := f.commits[commit].Files[name]
		out = append(out, map[string]string{"path": head, "type": "blob", "mode": file.Mode, "sha": GitBlobID(commit, file.Content)})
	}
	return out, true
}

func (f *forge) changed(id string) []map[string]string {
	c := f.commits[id]
	parent := f.commits[c.Parents[0]].Files
	out := []map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Files)) {
		if old, ok := parent[name]; !ok || old != c.Files[name] {
			out = append(out, map[string]string{"filename": name, "old_path": name, "new_path": name})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(parent)) {
		if _, ok := c.Files[name]; !ok {
			out = append(out, map[string]string{"filename": name, "old_path": name, "new_path": name})
		}
	}
	return out
}

func (f *forge) review(w http.ResponseWriter, branch string, create bool) {
	number, exists := f.reviews[branch]
	if !exists && !create {
		if f.kind == "gitea" {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, []any{})
		return
	}
	if !exists {
		number = len(f.reviews) + 1
		f.reviews[branch] = number
		f.writes = append(f.writes, "review")
	}
	web := "https://github.com/team/app/pull/" + strconv.Itoa(number)
	entry := map[string]any{"number": number, "state": "open", "html_url": web, "head": map[string]any{"ref": branch, "repo": map[string]int{"id": 12}}, "base": map[string]any{"ref": "main"}}
	if f.kind == "gitlab" {
		entry = map[string]any{"iid": number, "state": "opened", "web_url": web, "source_branch": branch, "target_branch": "main", "source_project_id": 12, "target_project_id": 12}
	}
	if create || f.kind == "gitea" {
		_ = stablejson.Encode(w, entry)
		return
	}
	_ = stablejson.Encode(w, []any{entry})
}

func (f *forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.down != 0 {
		w.WriteHeader(f.down)
		return
	}
	if r.URL.Path == "/team/app.git/info/refs" {
		_, password, _ := r.BasicAuth()
		if r.URL.Query().Get("service") != "git-receive-pack" {
			w.WriteHeader(400)
		} else if password == forgeWriter {
			w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		} else if password == forgeReader {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(401)
		}
		return
	}
	token := map[string]string{"github": "Bearer " + forgeWriter, "gitlab": forgeWriter, "gitea": "token " + forgeWriter}[f.kind]
	if r.Header.Get("Authorization") != token && r.Header.Get("PRIVATE-TOKEN") != token {
		w.WriteHeader(401)
		return
	}
	prefix := map[string]string{"github": "/repos/team/app", "gitlab": "/api/v4/projects/team/app", "gitea": "/api/v1/repos/team/app"}[f.kind]
	route, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		w.WriteHeader(404)
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost && json.UnmarshalRead(r.Body, &body) != nil {
		w.WriteHeader(400)
		return
	}
	switch {
	case route == "" && r.Method == http.MethodGet:
		_ = stablejson.Encode(w, map[string]any{"id": 12, "private": true, "visibility": "private", "owner": map[string]string{"visibility": "public"}})
	case strings.HasPrefix(route, "/branches/") || strings.HasPrefix(route, "/repository/branches/"):
		name := route[strings.Index(route, "/branches/")+len("/branches/"):]
		if f.kind == "gitea" && r.Method == http.MethodGet && strings.HasPrefix(name, "main/") {
			// Gitea's lookup of a review by base and head.
			if branch := strings.TrimPrefix(name, "main/"); f.reviews[branch] > 0 {
				f.review(w, branch, false)
				return
			}
			w.WriteHeader(404)
			return
		}
		head, exists := f.branches[name]
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": head, "id": head}})
	case f.kind == "gitea" && strings.HasPrefix(route, "/pulls/main/"):
		f.review(w, strings.TrimPrefix(route, "/pulls/main/"), false)
	case strings.HasPrefix(route, "/contents/") && r.Method == http.MethodGet, strings.HasPrefix(route, "/repository/files/"):
		name := strings.TrimPrefix(strings.TrimPrefix(route, "/contents/"), "/repository/files/")
		ref := r.URL.Query().Get("ref")
		file, exists := f.commits[ref].Files[name]
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]string{"path": name, "sha": GitBlobID(ref, file.Content), "file_path": name, "blob_id": GitBlobID(ref, file.Content), "commit_id": ref})
	case strings.HasPrefix(route, "/git/trees/"):
		entries, exists := f.entries(strings.TrimPrefix(route, "/git/trees/"))
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]any{"tree": entries, "total_count": len(entries)})
	case f.kind == "github" && strings.HasPrefix(route, "/git/commits/"):
		id := strings.TrimPrefix(route, "/git/commits/")
		if _, exists := f.commits[id]; !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]any{"sha": id, "tree": map[string]string{"sha": f.treeID(id, "")}})
	case f.kind == "github" && route == "/git/trees":
		root, exists := f.dirs[body["base_tree"].(string)]
		if !exists || root[1] != "" {
			w.WriteHeader(422)
			return
		}
		files := maps.Clone(f.commits[root[0]].Files)
		for _, raw := range body["tree"].([]any) {
			entry := raw.(map[string]any)
			name := entry["path"].(string)
			if entry["sha"] == nil && entry["content"] == nil {
				delete(files, name)
				continue
			}
			files[name] = forgeFile{Content: entry["content"].(string), Mode: entry["mode"].(string)}
		}
		id := objectID("tree", files)
		f.built[id] = files
		_ = stablejson.Encode(w, map[string]string{"sha": id})
	case f.kind == "github" && route == "/git/commits":
		parents := []string{}
		for _, p := range body["parents"].([]any) {
			parents = append(parents, p.(string))
		}
		id := f.commit(forgeCommit{Parents: parents, Message: body["message"].(string), Files: f.built[body["tree"].(string)]})
		f.writes = append(f.writes, "commit")
		_ = stablejson.Encode(w, map[string]string{"sha": id})
	case f.kind == "github" && route == "/git/refs":
		name := strings.TrimPrefix(body["ref"].(string), "refs/heads/")
		if _, exists := f.branches[name]; exists {
			w.WriteHeader(422)
			return
		}
		f.branches[name] = body["sha"].(string)
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{})
	case f.kind == "gitlab" && route == "/repository/commits":
		name := body["branch"].(string)
		if _, exists := f.branches[name]; exists {
			w.WriteHeader(400)
			return
		}
		start := body["start_sha"].(string)
		files := maps.Clone(f.commits[start].Files)
		for _, raw := range body["actions"].([]any) {
			action := raw.(map[string]any)
			name := action["file_path"].(string)
			_, exists := files[name]
			switch action["action"] {
			case "delete":
				if !exists {
					w.WriteHeader(400)
					return
				}
				delete(files, name)
			case "create", "update":
				if exists != (action["action"] == "update") {
					w.WriteHeader(400)
					return
				}
				content, _ := base64.StdEncoding.DecodeString(action["content"].(string))
				mode := files[name].Mode
				if mode == "" {
					mode = "100644"
				}
				files[name] = forgeFile{Content: string(content), Mode: mode}
			}
		}
		id := f.commit(forgeCommit{Parents: []string{start}, Message: body["commit_message"].(string), Files: files})
		f.branches[name] = id
		f.writes = append(f.writes, "commit")
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{"id": id})
	case f.kind == "gitlab" && strings.HasPrefix(route, "/repository/commits/"):
		id, diff := strings.CutSuffix(strings.TrimPrefix(route, "/repository/commits/"), "/diff")
		c, exists := f.commits[id]
		if !exists {
			w.WriteHeader(404)
			return
		}
		if diff {
			_ = stablejson.Encode(w, f.changed(id))
			return
		}
		_ = stablejson.Encode(w, map[string]any{"id": id, "message": c.Message, "parent_ids": c.Parents})
	case (f.kind == "github" && strings.HasPrefix(route, "/commits/")) || (f.kind == "gitea" && strings.HasPrefix(route, "/git/commits/")):
		id := strings.TrimPrefix(strings.TrimPrefix(route, "/commits/"), "/git/commits/")
		c, exists := f.commits[id]
		if !exists {
			w.WriteHeader(404)
			return
		}
		parents := []map[string]string{}
		for _, p := range c.Parents {
			parents = append(parents, map[string]string{"sha": p})
		}
		_ = stablejson.Encode(w, map[string]any{"sha": id, "commit": map[string]string{"message": c.Message}, "parents": parents, "files": f.changed(id)})
	case f.kind == "gitea" && route == "/branches" && r.Method == http.MethodPost:
		name := body["new_branch_name"].(string)
		if _, exists := f.branches[name]; exists {
			w.WriteHeader(409)
			return
		}
		f.branches[name] = body["old_ref_name"].(string)
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{})
	case f.kind == "gitea" && route == "/contents" && r.Method == http.MethodPost:
		name := body["branch"].(string)
		head, exists := f.branches[name]
		if !exists || body["new_branch"] != nil {
			w.WriteHeader(404)
			return
		}
		files := maps.Clone(f.commits[head].Files)
		for _, raw := range body["files"].([]any) {
			change := raw.(map[string]any)
			name := change["path"].(string)
			old, exists := files[name]
			if exists && change["sha"] != GitBlobID(head, old.Content) || !exists && change["operation"] != "create" {
				w.WriteHeader(409)
				return
			}
			if change["operation"] == "delete" {
				delete(files, name)
				continue
			}
			content, _ := base64.StdEncoding.DecodeString(change["content"].(string))
			mode := old.Mode
			if mode == "" {
				mode = "100644"
			}
			files[name] = forgeFile{Content: string(content), Mode: mode}
		}
		id := f.commit(forgeCommit{Parents: []string{head}, Message: body["message"].(string), Files: files})
		f.branches[name] = id
		f.writes = append(f.writes, "commit")
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": id}})
	case route == "/pulls" || route == "/merge_requests":
		if r.Method == http.MethodPost {
			head, _ := body["head"].(string)
			if f.kind == "gitlab" {
				head = body["source_branch"].(string)
			}
			if _, exists := f.reviews[head]; exists && f.kind == "gitea" {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(201)
			f.review(w, head, true)
			return
		}
		branch := r.URL.Query().Get("source_branch")
		if f.kind == "github" {
			branch = strings.TrimPrefix(r.URL.Query().Get("head"), "team:")
		}
		f.review(w, branch, false)
	default:
		w.WriteHeader(404)
	}
}

func forgeProvider(t *testing.T, f *forge, password string) *Provider {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	c := Connection{Provider: f.kind, BaseURL: server.URL, Repository: "team/app", RepositoryID: 12, Private: true}
	policy := Policy{Development: true}
	if f.kind == "github" {
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
			f, base := newForge(kind, map[string]forgeFile{
				"compose.yaml":   {"services: {}\n", "100644"},
				"deploy/run.sh":  {"#!/bin/sh\n", "100755"},
				"deploy/old.yml": {"old: true\n", "100644"},
				"README.md":      {"keep me\n", "100644"},
			})
			p := forgeProvider(t, f, forgeWriter)
			s := Submission{ID: uuid.New(), Base: base, Branch: "main", At: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
				Previous: []string{"compose.yaml", "deploy/run.sh", "deploy/old.yml"},
				Files:    []composeproject.File{{Path: "compose.yaml", Content: "services: {web: {}}\n"}, {Path: "deploy/run.sh", Content: "#!/bin/sh\necho changed\n"}, {Path: "deploy/new.yml", Content: "new: true\n"}}}
			result, err := p.Submit(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			branch := "astilba/" + s.ID.String()
			if f.branches[branch] != result.Commit || f.branches["main"] != base || result.Review.Number != f.reviews[branch] {
				t.Fatalf("not one branch and review: %+v", result)
			}
			got := f.commits[result.Commit]
			want := map[string]forgeFile{"compose.yaml": {"services: {web: {}}\n", "100644"}, "deploy/run.sh": {"#!/bin/sh\necho changed\n", "100755"}, "deploy/new.yml": {"new: true\n", "100644"}, "README.md": {"keep me\n", "100644"}}
			if !slices.Equal(got.Parents, []string{base}) || !maps.Equal(got.Files, want) || !strings.Contains(got.Message, "Astilba-Submission: "+s.ID.String()) {
				t.Fatalf("the commit is not exactly the draft on its base: %+v", got)
			}
			writes := len(f.writes)
			again, err := p.Submit(t.Context(), s)
			if err != nil || again != result || len(f.writes) != writes {
				t.Fatalf("a retry did not find the same branch and review: %+v %v %v", again, err, f.writes[writes:])
			}

			// A branch of the submission's name that holds another change.
			other := s
			other.ID = uuid.New()
			// Exactly this draft's change, and one more file.
			files := maps.Clone(want)
			files["extra.txt"] = forgeFile{"not ours\n", "100644"}
			f.branches["astilba/"+other.ID.String()] = f.commit(forgeCommit{Parents: []string{base}, Message: other.message(), Files: files})
			var refused *SubmissionRefused
			if _, err = p.Submit(t.Context(), other); !errors.As(err, &refused) || f.reviews["astilba/"+other.ID.String()] != 0 {
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
			f.branches["main"] = f.commit(forgeCommit{Parents: []string{base}, Message: "moved", Files: f.commits[base].Files})
			if _, err = p.Submit(t.Context(), moved); !errors.As(err, &refused) || f.branches["astilba/"+moved.ID.String()] != "" {
				t.Fatalf("a draft was submitted onto a moved branch: %v", err)
			}
			if SubmissionRetryable(err) {
				t.Fatal("a moved branch was taken for a passing failure")
			}
			f.down = http.StatusBadGateway
			if _, err = p.Submit(t.Context(), moved); err == nil || !SubmissionRetryable(err) {
				t.Fatalf("a failing provider was not retried: %v", err)
			}
		})
	}
}

// A runner receives only a credential the Git server will not let push. The server is asked for
// its receive-pack advertisement, which every provider refuses without write access.
func TestCanPushAsksTheGitServer(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := newForge(kind, map[string]forgeFile{"compose.yaml": {"services: {}\n", "100644"}})
			if can, err := forgeProvider(t, f, forgeWriter).CanPush(t.Context()); err != nil || !can {
				t.Fatalf("a write credential was taken for read-only: %t %v", can, err)
			}
			reader := forgeProvider(t, f, forgeReader)
			if can, err := reader.CanPush(t.Context()); err != nil || can {
				t.Fatalf("a read credential was taken for one that can push: %t %v", can, err)
			}
			// A credential the server didn't accept says nothing about what it could do.
			if _, err := forgeProvider(t, f, "not-a-token").CanPush(t.Context()); !errors.Is(err, ErrCredentialRejected) {
				t.Fatalf("an unaccepted credential was taken for read-only: %v", err)
			}
			f.down = http.StatusInternalServerError
			if _, err := reader.CanPush(t.Context()); err == nil {
				t.Fatal("an unknown answer was taken for read-only")
			}
		})
	}
	// Redirects are not followed, so a redirect to a page that answers 200 proves nothing.
	server := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer server.Close()
	p, _ := NewProvider(Connection{Provider: "gitea", BaseURL: server.URL, Repository: "team/app"}, Credential{Password: forgeReader}, Policy{Development: true})
	if _, err := p.CanPush(context.Background()); err == nil {
		t.Fatal("a redirect was taken for an answer")
	}
}
