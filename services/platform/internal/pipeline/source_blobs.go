package pipeline

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
)

// GitBlobID is the object name Git gives a file holding content, in the object format of
// commit: a SHA-256 repository names its commits with 64 characters. Matching one means
// finding a second preimage, which a chosen-prefix collision attack on SHA-1 does not give.
func GitBlobID(commit, content string) string {
	object := []byte("blob " + strconv.Itoa(len(content)) + "\x00" + content)
	if len(commit) == 64 {
		sum := sha256.Sum256(object)
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(object)
	return hex.EncodeToString(sum[:])
}

// SourceMatches reports whether files are what the provider said this import's paths hold.
// The object name binds a file's whole contents, so a runner cannot change them and still match.
func (c Command) SourceMatches(files []composeproject.File) bool {
	for _, file := range files {
		if c.SourceBlobs[file.Path] != GitBlobID(c.Commit, file.Content) {
			return false
		}
	}
	return true
}

// SourceBlobs asks the provider for each path's Git blob at the exact commit. A path with no
// file there is absent from the answer, while an error means the provider gave no answer.
// A whole repository's tree can exceed what any provider returns in one response, so this asks
// only about a source's few selected files. The first failure ends the asking, so an exhausted
// rate limit costs one more request.
func (p *Provider) SourceBlobs(ctx context.Context, commit string, paths []string) (map[string]string, error) {
	blobs := make(map[string]string, len(paths))
	walk := treeWalk{directories: map[string]map[string]treeEntry{}}
	for _, name := range paths {
		blob, found, err := p.sourceBlob(ctx, commit, name, &walk)
		if err != nil {
			return nil, err
		}
		if found {
			blobs[name] = blob
		}
	}
	return blobs, nil
}

func (p *Provider) sourceBlob(ctx context.Context, commit, name string, walk *treeWalk) (string, bool, error) {
	var blob string
	var found bool
	var err error
	if p.Connection.Provider == "gitea" {
		blob, found, err = p.treeBlob(ctx, commit, name, walk)
	} else {
		blob, found, err = p.fileBlob(ctx, commit, name)
	}
	var status *ProviderError
	// A file this large cannot be imported anyway. The runner gives the reason, and a blocked
	// refresh would help nobody.
	if err != nil && (errors.As(err, &status) && status.Status == http.StatusNotFound || errors.Is(err, errProviderResponseSize)) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if found && (len(blob) != len(commit) || !ValidCommit(blob)) {
		return "", false, errors.New("provider omitted exact blob")
	}
	return blob, found, nil
}

// fileBlob names the entry GitHub or GitLab holds at the path without reading its contents: the
// object name binds them, and also what a large-file pointer or a symlink is, whichever way a
// provider presents one. A symlink or submodule can therefore match at most its own recorded
// bytes, not contents a runner chose. Both providers refuse a ref named like a commit, so ?ref=
// can only mean the commit.
func (p *Provider) fileBlob(ctx context.Context, commit, name string) (string, bool, error) {
	var endpoint string
	if p.Connection.Provider == "gitlab" {
		// PathEscape leaves + alone, which a query-style decoder would read as a space.
		endpoint = p.apiPath() + "/repository/files/" + strings.ReplaceAll(url.PathEscape(name), "+", "%2B")
	} else {
		segments := strings.Split(name, "/")
		for i, segment := range segments {
			segments[i] = url.PathEscape(segment)
		}
		endpoint = p.apiPath() + "/contents/" + strings.Join(segments, "/")
	}
	var raw jsontext.Value
	if err := p.request(ctx, http.MethodGet, endpoint+"?ref="+url.QueryEscape(commit), nil, &raw); err != nil {
		return "", false, err
	}
	// GitHub answers a directory with a list of its entries.
	if raw.Kind() != '{' {
		return "", false, nil
	}
	var entry struct {
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		FilePath string `json:"file_path"`
		BlobID   string `json:"blob_id"`
		CommitID string `json:"commit_id"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return "", false, errors.New("invalid provider response")
	}
	if p.Connection.Provider == "gitlab" {
		if entry.CommitID != commit {
			return "", false, errors.New("provider answered about another commit")
		}
		return entry.BlobID, entry.FilePath == name, nil
	}
	return entry.SHA, entry.Path == name, nil
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// treeWalkPages bounds the memory and requests one answer can cost, whatever a server's listing
// says. A walk longer than this gives no answer, so its files are not imported.
const treeWalkPages = 64

// treeWalk holds the directories read for one answer, which many files in them would repeat.
type treeWalk struct {
	directories map[string]map[string]treeEntry
	pages       int
}

// treeBlob walks Gitea's trees from the commit. Gitea's contents API resolves ?ref= as a branch,
// then a tag, then a commit, and anyone who can push may name a ref after a commit, so that
// answer can be about other files. A tree is asked for by its object name alone.
func (p *Provider) treeBlob(ctx context.Context, commit, name string, walk *treeWalk) (string, bool, error) {
	segments := strings.Split(name, "/")
	id := commit
	for i, segment := range segments {
		entries, err := p.treeEntries(ctx, id, walk)
		if err != nil {
			return "", false, err
		}
		entry, found := entries[segment]
		if !found {
			return "", false, nil
		}
		if i == len(segments)-1 {
			return entry.SHA, true, nil
		}
		if entry.Type != "tree" {
			return "", false, nil
		}
		id = entry.SHA
	}
	return "", false, nil
}

func (p *Provider) treeEntries(ctx context.Context, id string, walk *treeWalk) (map[string]treeEntry, error) {
	if entries, known := walk.directories[id]; known {
		return entries, nil
	}
	if !ValidCommit(id) {
		return nil, errors.New("provider omitted exact tree")
	}
	entries := map[string]treeEntry{}
	for page := 1; ; page++ {
		walk.pages++
		if walk.pages > treeWalkPages {
			return nil, errors.New("provider tree exceeds its limit")
		}
		var data struct {
			Tree       []treeEntry `json:"tree"`
			TotalCount int         `json:"total_count"`
		}
		if err := p.request(ctx, http.MethodGet, p.apiPath()+"/git/trees/"+id+"?per_page=1000&page="+strconv.Itoa(page), nil, &data); err != nil {
			return nil, err
		}
		for _, entry := range data.Tree {
			entries[entry.Path] = entry
		}
		if len(data.Tree) == 0 || len(entries) >= data.TotalCount {
			walk.directories[id] = entries
			return entries, nil
		}
	}
}
