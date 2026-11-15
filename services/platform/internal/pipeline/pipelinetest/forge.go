// Package pipelinetest provides an in-memory Git provider for tests of submissions.
package pipelinetest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// File is a file in a fake repository's commit.
type File struct{ Content, Mode string }

// Commit is a commit in a fake repository.
type Commit struct {
	Parents []string
	Message string
	Files   map[string]File
}

// Forge is an in-memory repository, team/app with ID 12, answering the parts of GitHub's,
// GitLab's and Gitea's APIs that submissions use, as their documentation and a real Gitea
// describe them. It enforces what the submission relies on: create-only branches, a commit's own
// parents and per-file blob guards. Its fields are read and written under Lock, or between
// requests.
type Forge struct {
	kind    string
	mu      sync.Mutex
	held    *hold
	Commits map[string]Commit
	// dirs names each directory of a commit a client may ask for, and built each tree a
	// GitHub client made.
	dirs     map[string][2]string
	built    map[string]map[string]File
	Branches map[string]string
	Reviews  map[string]int
	Writes   []string
	// Down answers every request with this status while it is set.
	Down int
	// Version is what a Gitea says it runs.
	Version string
}

// Writer is the API credential a Forge accepts, which can also push; Reader can only clone.
const (
	Writer = "writer-token"
	Reader = "reader-token"
)

// NewForge returns a Forge of kind github, gitlab or gitea whose main branch holds files, and
// that commit.
func NewForge(kind string, files map[string]File) (*Forge, string) {
	f := &Forge{kind: kind, Version: "1.22.6", Commits: map[string]Commit{}, dirs: map[string][2]string{}, built: map[string]map[string]File{}, Branches: map[string]string{}, Reviews: map[string]int{}}
	base := f.AddCommit(Commit{Message: "base", Files: files})
	f.Branches["main"] = base
	return f, base
}

func objectID(parts ...any) string {
	raw, _ := stablejson.Marshal(parts)
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}

func (f *Forge) AddCommit(c Commit) string {
	id := objectID("commit", c)
	f.Commits[id] = c
	return id
}

// treeID names the directory dir of a commit, as an object name a client can ask for.
func (f *Forge) treeID(commit, dir string) string {
	id := objectID("tree", commit, dir)
	f.dirs[id] = [2]string{commit, dir}
	return id
}

func (f *Forge) entries(id string) ([]map[string]string, bool) {
	commit, dir := id, ""
	if _, ok := f.Commits[id]; !ok {
		named, ok := f.dirs[id]
		if !ok {
			return nil, false
		}
		commit, dir = named[0], named[1]
	}
	seen := map[string]bool{}
	out := []map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(f.Commits[commit].Files)) {
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
		file := f.Commits[commit].Files[name]
		out = append(out, map[string]string{"path": head, "type": "blob", "mode": file.Mode, "sha": blobID(commit, file.Content)})
	}
	return out, true
}

func (f *Forge) changed(id string) []map[string]string {
	c := f.Commits[id]
	parent := f.Commits[c.Parents[0]].Files
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

func (f *Forge) review(w http.ResponseWriter, r *http.Request, branch string, create bool) {
	number, exists := f.Reviews[branch]
	if !exists && !create {
		if f.kind == "gitea" {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, []any{})
		return
	}
	if !exists {
		number = len(f.Reviews) + 1
		f.Reviews[branch] = number
		f.Writes = append(f.Writes, "review")
	}
	// A review links to the provider's own site, as reviewURLMatches requires.
	web := "http://" + r.Host + "/team/app/pulls/" + strconv.Itoa(number)
	if f.kind == "github" {
		web = "https://github.com/team/app/pull/" + strconv.Itoa(number)
	}
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

func (f *Forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.wait(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.Down != 0 {
		w.WriteHeader(f.Down)
		return
	}
	if r.URL.Path == "/team/app.git/info/refs" {
		_, password, _ := r.BasicAuth()
		if r.URL.Query().Get("service") != "git-receive-pack" {
			w.WriteHeader(400)
		} else if password == Writer {
			w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		} else if password == Reader {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(401)
		}
		return
	}
	token := map[string]string{"github": "Bearer " + Writer, "gitlab": Writer, "gitea": "token " + Writer}[f.kind]
	if r.Header.Get("Authorization") != token && r.Header.Get("PRIVATE-TOKEN") != token {
		w.WriteHeader(401)
		return
	}
	if f.kind == "gitea" && r.URL.Path == "/api/v1/version" {
		_ = stablejson.Encode(w, map[string]string{"version": f.Version})
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
			if branch := strings.TrimPrefix(name, "main/"); f.Reviews[branch] > 0 {
				f.review(w, r, branch, false)
				return
			}
			w.WriteHeader(404)
			return
		}
		head, exists := f.Branches[name]
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": head, "id": head}})
	case f.kind == "gitea" && strings.HasPrefix(route, "/pulls/main/"):
		f.review(w, r, strings.TrimPrefix(route, "/pulls/main/"), false)
	case strings.HasPrefix(route, "/contents/") && r.Method == http.MethodGet, strings.HasPrefix(route, "/repository/files/"):
		name := strings.TrimPrefix(strings.TrimPrefix(route, "/contents/"), "/repository/files/")
		ref := r.URL.Query().Get("ref")
		file, exists := f.Commits[ref].Files[name]
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]string{"path": name, "sha": blobID(ref, file.Content), "file_path": name, "blob_id": blobID(ref, file.Content), "commit_id": ref})
	case strings.HasPrefix(route, "/git/trees/"):
		entries, exists := f.entries(strings.TrimPrefix(route, "/git/trees/"))
		if !exists {
			w.WriteHeader(404)
			return
		}
		_ = stablejson.Encode(w, map[string]any{"tree": entries, "total_count": len(entries)})
	case f.kind == "github" && strings.HasPrefix(route, "/git/commits/"):
		id := strings.TrimPrefix(route, "/git/commits/")
		if _, exists := f.Commits[id]; !exists {
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
		files := maps.Clone(f.Commits[root[0]].Files)
		for _, raw := range body["tree"].([]any) {
			entry := raw.(map[string]any)
			name := entry["path"].(string)
			if entry["sha"] == nil && entry["content"] == nil {
				delete(files, name)
				continue
			}
			files[name] = File{Content: entry["content"].(string), Mode: entry["mode"].(string)}
		}
		id := objectID("tree", files)
		f.built[id] = files
		_ = stablejson.Encode(w, map[string]string{"sha": id})
	case f.kind == "github" && route == "/git/commits":
		parents := []string{}
		for _, p := range body["parents"].([]any) {
			parents = append(parents, p.(string))
		}
		id := f.AddCommit(Commit{Parents: parents, Message: body["message"].(string), Files: f.built[body["tree"].(string)]})
		f.Writes = append(f.Writes, "commit")
		_ = stablejson.Encode(w, map[string]string{"sha": id})
	case f.kind == "github" && route == "/git/refs":
		name := strings.TrimPrefix(body["ref"].(string), "refs/heads/")
		if _, exists := f.Branches[name]; exists {
			w.WriteHeader(422)
			return
		}
		f.Branches[name] = body["sha"].(string)
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{})
	case f.kind == "gitlab" && route == "/repository/commits":
		name := body["branch"].(string)
		if _, exists := f.Branches[name]; exists {
			w.WriteHeader(400)
			return
		}
		start := body["start_sha"].(string)
		files := maps.Clone(f.Commits[start].Files)
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
				files[name] = File{Content: string(content), Mode: mode}
			}
		}
		id := f.AddCommit(Commit{Parents: []string{start}, Message: body["commit_message"].(string), Files: files})
		f.Branches[name] = id
		f.Writes = append(f.Writes, "commit")
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{"id": id})
	case f.kind == "gitlab" && strings.HasPrefix(route, "/repository/commits/"):
		id, diff := strings.CutSuffix(strings.TrimPrefix(route, "/repository/commits/"), "/diff")
		c, exists := f.Commits[id]
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
		c, exists := f.Commits[id]
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
		if _, exists := f.Branches[name]; exists {
			w.WriteHeader(409)
			return
		}
		f.Branches[name] = body["old_ref_name"].(string)
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]string{})
	case f.kind == "gitea" && route == "/contents" && r.Method == http.MethodPost:
		name := body["branch"].(string)
		head, exists := f.Branches[name]
		if !exists || body["new_branch"] != nil {
			w.WriteHeader(404)
			return
		}
		files := maps.Clone(f.Commits[head].Files)
		for _, raw := range body["files"].([]any) {
			change := raw.(map[string]any)
			name := change["path"].(string)
			old, exists := files[name]
			if exists && change["sha"] != blobID(head, old.Content) || !exists && change["operation"] != "create" {
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
			files[name] = File{Content: string(content), Mode: mode}
		}
		id := f.AddCommit(Commit{Parents: []string{head}, Message: body["message"].(string), Files: files})
		f.Branches[name] = id
		f.Writes = append(f.Writes, "commit")
		w.WriteHeader(201)
		_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": id}})
	case route == "/pulls" || route == "/merge_requests":
		if r.Method == http.MethodPost {
			head, _ := body["head"].(string)
			if f.kind == "gitlab" {
				head = body["source_branch"].(string)
			}
			if _, exists := f.Reviews[head]; exists && f.kind == "gitea" {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(201)
			f.review(w, r, head, true)
			return
		}
		branch := r.URL.Query().Get("source_branch")
		if f.kind == "github" {
			branch = strings.TrimPrefix(r.URL.Query().Get("head"), "team:")
		}
		f.review(w, r, branch, false)
	default:
		w.WriteHeader(404)
	}
}

// Kind is the provider the Forge answers as.
func (f *Forge) Kind() string { return f.kind }

// Lock holds the Forge's state still for a test that reads or changes it while requests may
// arrive, and returns its release.
func (f *Forge) Lock() func() {
	f.mu.Lock()
	return f.mu.Unlock
}

type hold struct {
	method, path string
	entered      chan struct{}
	release      chan struct{}
}

// Hold stops the next request with method whose path ends with path before the Forge answers it.
// entered is closed when that request arrives, and release lets it through. Requests after it pass.
func (f *Forge) Hold(method, path string) (entered <-chan struct{}, release func()) {
	h := &hold{method: method, path: path, entered: make(chan struct{}), release: make(chan struct{})}
	f.mu.Lock()
	f.held = h
	f.mu.Unlock()
	return h.entered, sync.OnceFunc(func() { close(h.release) })
}

func (f *Forge) wait(r *http.Request) {
	f.mu.Lock()
	h := f.held
	if h == nil || r.Method != h.method || !strings.HasSuffix(r.URL.Path, h.path) {
		f.mu.Unlock()
		return
	}
	f.held = nil
	f.mu.Unlock()
	close(h.entered)
	<-h.release
}

// blobID is the Git object name of content, in the hash a commit named commit uses.
func blobID(commit, content string) string {
	object := []byte("blob " + strconv.Itoa(len(content)) + "\x00" + content)
	if len(commit) == 64 {
		sum := sha256.Sum256(object)
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(object)
	return hex.EncodeToString(sum[:])
}
