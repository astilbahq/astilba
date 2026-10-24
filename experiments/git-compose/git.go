// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxFileBytes = 1 << 20

type Repository struct {
	provider                *Provider
	dir, name, branch, base string
	env                     []string
	Source                  Bundle
}

type Draft struct {
	commit string
	Diff   string
}

type Publication struct {
	Commit string
	Branch string
	Review Review
}

var ErrConflict = errors.New("repository changed; reconcile the draft")

func validCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4*maxFileBytes {
		return 0, errors.New("Git output exceeds fixture limit")
	}
	return b.Buffer.Write(p)
}

func (r *Repository) git(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	cmd.Stdin = bytes.NewReader(input)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Git %s failed", args[0])
	}
	return output.Bytes(), nil
}

func newGitRepository(p *Provider, dir, name, branch string) *Repository {
	r := &Repository{provider: p, dir: dir, name: name, branch: branch}
	r.env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http", "GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=http." + strings.TrimSuffix(p.gitURL(name), ".git") + ".git.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(p.login+":"+p.token)),
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=core.hooksPath", "GIT_CONFIG_VALUE_2=/dev/null", "GIT_CONFIG_KEY_3=credential.helper", "GIT_CONFIG_VALUE_3=",
		"GIT_AUTHOR_NAME=Astilba fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Astilba fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_INDEX_FILE=" + filepath.Join(dir, "index"),
		"GIT_AUTHOR_DATE=2026-10-22T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-22T00:00:00Z",
	}
	return r
}

func Open(ctx context.Context, p *Provider, dir, name, branch string, paths []string) (*Repository, error) {
	if _, err := p.RepositoryID(ctx, name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r := newGitRepository(p, dir, name, branch)
	if _, err := r.git(ctx, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return nil, errors.New("invalid branch")
	}
	if _, err := r.git(ctx, nil, "init", "--bare", "--quiet"); err != nil {
		return nil, err
	}
	if _, err := r.git(ctx, nil, "remote", "add", "origin", p.gitURL(name)); err != nil {
		return nil, err
	}
	if _, err := r.git(ctx, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "origin", "refs/heads/"+branch); err != nil {
		return nil, err
	}
	base, err := r.git(ctx, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	r.base = strings.TrimSpace(string(base))
	if !validCommit(r.base) {
		return nil, errors.New("invalid imported commit")
	}
	for _, path := range paths {
		if !validPath(path) {
			return nil, errors.New("invalid source path")
		}
		entry, err := r.git(ctx, nil, "ls-tree", "-z", r.base, "--", path)
		if err != nil {
			return nil, err
		}
		fields := strings.SplitN(strings.TrimSuffix(string(entry), "\x00"), "\t", 2)
		if len(fields) != 2 || fields[1] != path || !strings.HasPrefix(fields[0], "100644 blob ") {
			return nil, errors.New("source must be an ordinary repository file")
		}
		data, err := r.git(ctx, nil, "show", r.base+":"+path)
		if err != nil || len(data) > maxFileBytes {
			return nil, errors.New("cannot read bounded source file")
		}
		r.Source.Files = append(r.Source.Files, SourceFile{Path: path, Data: data})
	}
	return r, nil
}

func (r *Repository) Base() string { return r.base }

func (r *Repository) remoteHead(ctx context.Context, branch string) (string, error) {
	data, err := r.git(ctx, nil, "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !validCommit(fields[0]) {
		return "", errors.New("invalid remote reference")
	}
	return fields[0], nil
}

func (r *Repository) Prepare(ctx context.Context, edited Bundle) (Draft, error) {
	// Removed lines and context also enter the diff shown for review.
	if err := r.Source.validate(); err != nil {
		return Draft{}, err
	}
	for _, file := range edited.Files {
		if err := rejectSecrets(file.Data, r.Source.Secrets); err != nil {
			return Draft{}, err
		}
	}
	if err := edited.validate(); err != nil {
		return Draft{}, err
	}
	if _, err := r.git(ctx, nil, "read-tree", r.base); err != nil {
		return Draft{}, err
	}
	original := map[string][]byte{}
	for _, file := range r.Source.Files {
		original[file.Path] = file.Data
	}
	files := append([]SourceFile(nil), edited.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	changed := false
	for _, file := range files {
		old, ok := original[file.Path]
		if !ok {
			return Draft{}, errors.New("draft contains a file outside the imported source")
		}
		if bytes.Equal(old, file.Data) {
			continue
		}
		changed = true
		blob, err := r.git(ctx, file.Data, "hash-object", "-w", "--stdin")
		if err != nil {
			return Draft{}, err
		}
		if _, err := r.git(ctx, nil, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(string(blob)), file.Path); err != nil {
			return Draft{}, err
		}
	}
	if !changed {
		return Draft{}, errors.New("draft has no source changes")
	}
	tree, err := r.git(ctx, nil, "write-tree")
	if err != nil {
		return Draft{}, err
	}
	commit, err := r.git(ctx, []byte("Update Compose images\n"), "commit-tree", strings.TrimSpace(string(tree)), "-p", r.base)
	if err != nil {
		return Draft{}, err
	}
	sha := strings.TrimSpace(string(commit))
	diff, err := r.git(ctx, nil, "diff", "--no-ext-diff", "--no-textconv", r.base, sha)
	if err != nil {
		return Draft{}, err
	}
	return Draft{commit: sha, Diff: string(diff)}, nil
}

func (r *Repository) Publish(ctx context.Context, draft Draft, branch string) (Publication, error) {
	result := Publication{Commit: draft.commit, Branch: branch}
	if !validCommit(draft.commit) || branch == r.branch {
		return result, errors.New("invalid review branch")
	}
	if _, err := r.git(ctx, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return result, errors.New("invalid review branch")
	}
	current, err := r.remoteHead(ctx, r.branch)
	if err != nil {
		return result, err
	}
	if current != r.base {
		return result, ErrConflict
	}
	// An empty expected value means the branch must not exist, even if a
	// previously published commit would otherwise permit a fast-forward.
	if _, err := r.git(ctx, nil, "push", "--quiet", "--force-with-lease=refs/heads/"+branch+":", "origin", draft.commit+":refs/heads/"+branch); err != nil {
		return result, err
	}
	current, err = r.remoteHead(ctx, r.branch)
	if err != nil {
		return result, err
	}
	if current != r.base {
		return result, ErrConflict
	}
	result.Review, err = r.provider.openReview(ctx, r.name, r.branch, branch)
	return result, err
}
