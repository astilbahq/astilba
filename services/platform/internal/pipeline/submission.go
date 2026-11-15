package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/google/uuid"
)

// Submissions are written by management through the provider's API with the connection's API
// credential. A runner builds untrusted code, so it never holds a credential that can write.
const (
	submissionAuthor = "Astilba"
	submissionEmail  = "astilba@users.noreply.github.com"
)

// Submission is a saved draft's files to propose on top of the commit it was based on.
type Submission struct {
	ID     uuid.UUID
	Base   string
	Branch string
	Files  []composeproject.File
	// Previous are the paths the draft's source selected at Base. One the draft no longer has
	// is removed from the branch.
	Previous []string
	At       time.Time
}

func (s Submission) branch() string { return "astilba/" + s.ID.String() }
func (s Submission) message() string {
	return "Update deployment configuration\n\nAstilba-Submission: " + s.ID.String() + "\n"
}

type SubmissionResult struct {
	Commit string `json:"commit"`
	Review Review `json:"review"`
}

// SubmissionRefused is an answer that asking again cannot change.
type SubmissionRefused struct{ Reason string }

func (e *SubmissionRefused) Error() string { return e.Reason }

// SubmissionRetryable reports whether err may pass on a later attempt: the provider could not
// be reached, was failing, or asked for time.
func SubmissionRetryable(err error) bool {
	var refused *SubmissionRefused
	if errors.As(err, &refused) {
		return false
	}
	var status *ProviderError
	if errors.As(err, &status) {
		return status.Status >= 500 || status.Status == http.StatusTooManyRequests || status.Status == http.StatusRequestTimeout
	}
	return true
}

type fileChange struct {
	Path    string
	Content string
	Delete  bool
	// Blob is the file at the base commit, or empty when the change adds it.
	Blob string
}

func (s Submission) validate() error {
	if s.ID == uuid.Nil || !ValidCommit(s.Base) || !ValidBranch(s.Branch) || len(s.Files) == 0 || s.At.IsZero() {
		return errors.New("invalid submission")
	}
	for _, f := range append(slices.Clone(s.Previous), paths(s.Files)...) {
		if !composeproject.ValidPath(f) || strings.HasPrefix(f, ".git/") {
			return errors.New("invalid submission path")
		}
	}
	return nil
}

func paths(files []composeproject.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// Submit proposes the submission as one commit on a branch named after it, created only if it
// does not exist, then returns the open review of that branch into the base branch. A retry
// finds the branch and its review again. A branch already of that name is used only if it
// holds exactly this change, so a retry never proposes anything else under Astilba's name.
func (p *Provider) Submit(ctx context.Context, s Submission) (SubmissionResult, error) {
	var out SubmissionResult
	if err := s.validate(); err != nil {
		return out, err
	}
	if err := p.baseUnchanged(ctx, s, "The source branch has changed since this draft was imported. Refresh and reconcile the draft, then submit it again."); err != nil {
		return out, err
	}
	changes, err := p.submissionChanges(ctx, s)
	if err != nil {
		return out, err
	}
	if len(changes) == 0 {
		return out, &SubmissionRefused{"This draft has no changes from the branch."}
	}
	branch := s.branch()
	existing, err := p.Resolve(ctx, branch)
	found := err == nil
	if err != nil && !errors.Is(err, ErrBranchNotFound) {
		return out, err
	}
	// Gitea makes the branch at the base before committing to it, so an attempt can stop between.
	if !found || p.Connection.Provider == "gitea" && existing == s.Base {
		created, createErr := p.createSubmission(ctx, s, changes, found)
		if createErr == nil {
			existing = created
		} else {
			// An earlier attempt or another writer can have made the branch since it was looked for.
			existing, err = p.Resolve(ctx, branch)
			if err != nil || existing == s.Base {
				return out, createErr
			}
		}
	}
	if err = p.verifySubmission(ctx, s, changes, existing); err != nil {
		return out, err
	}
	if err = p.baseUnchanged(ctx, s, "The source branch changed before the review was opened. Refresh and reconcile the draft; submitting it again reuses branch "+branch+"."); err != nil {
		return out, err
	}
	review, err := p.Review(ctx, s.Branch, branch)
	if err != nil {
		return out, err
	}
	if review.Number < 1 {
		return out, errors.New("provider omitted review identity")
	}
	return SubmissionResult{Commit: existing, Review: review}, nil
}

func (p *Provider) baseUnchanged(ctx context.Context, s Submission, reason string) error {
	head, err := p.Resolve(ctx, s.Branch)
	if errors.Is(err, ErrBranchNotFound) {
		return &SubmissionRefused{"The source branch no longer exists."}
	}
	if err != nil {
		return err
	}
	if head != s.Base {
		return &SubmissionRefused{reason}
	}
	return nil
}

// submissionChanges compares the draft with what the base commit holds, leaving out files the
// draft did not change, as a commit made with Git would.
func (p *Provider) submissionChanges(ctx context.Context, s Submission) ([]fileChange, error) {
	written := map[string]bool{}
	all := []string{}
	for _, f := range s.Files {
		written[f.Path] = true
		all = append(all, f.Path)
	}
	for _, name := range s.Previous {
		if !written[name] {
			all = append(all, name)
		}
	}
	blobs, err := p.SourceBlobs(ctx, s.Base, all)
	if err != nil {
		return nil, err
	}
	changes := []fileChange{}
	for _, f := range s.Files {
		blob, exists := blobs[f.Path]
		if exists && blob == GitBlobID(s.Base, f.Content) {
			continue
		}
		changes = append(changes, fileChange{Path: f.Path, Content: f.Content, Blob: blob})
	}
	for _, name := range s.Previous {
		if blob, exists := blobs[name]; exists && !written[name] {
			changes = append(changes, fileChange{Path: name, Delete: true, Blob: blob})
		}
	}
	return changes, nil
}

func (p *Provider) createSubmission(ctx context.Context, s Submission, changes []fileChange, branchExists bool) (string, error) {
	switch p.Connection.Provider {
	case "github":
		return p.createGitHubSubmission(ctx, s, changes)
	case "gitlab":
		return p.createGitLabSubmission(ctx, s, changes)
	default:
		return p.createGiteaSubmission(ctx, s, changes, branchExists)
	}
}

// createGitHubSubmission builds the commit with the Git Data API: a tree over the base commit's,
// a commit with the base as its only parent, then a reference GitHub creates only if it is new.
func (p *Provider) createGitHubSubmission(ctx context.Context, s Submission, changes []fileChange) (string, error) {
	var base struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := p.request(ctx, http.MethodGet, p.apiPath()+"/git/commits/"+s.Base, nil, &base); err != nil {
		return "", err
	}
	if !ValidCommit(base.Tree.SHA) {
		return "", errors.New("provider omitted the base tree")
	}
	walk := treeWalk{directories: map[string]map[string]treeEntry{}}
	entries := []map[string]any{}
	for _, c := range changes {
		mode := "100644"
		if c.Blob != "" {
			entry, found, err := p.treeFile(ctx, s.Base, c.Path, &walk)
			if err != nil {
				return "", err
			}
			if !found || entry.Type != "blob" || entry.Mode != "100644" && entry.Mode != "100755" {
				return "", &SubmissionRefused{"Only regular files can be submitted, and " + c.Path + " is not one at the base commit."}
			}
			mode = entry.Mode
		}
		if c.Delete {
			entries = append(entries, map[string]any{"path": c.Path, "mode": mode, "type": "blob", "sha": nil})
		} else {
			entries = append(entries, map[string]any{"path": c.Path, "mode": mode, "type": "blob", "content": c.Content})
		}
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := p.request(ctx, http.MethodPost, p.apiPath()+"/git/trees", map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if !ValidCommit(tree.SHA) {
		return "", errors.New("provider omitted the new tree")
	}
	if tree.SHA == base.Tree.SHA {
		return "", &SubmissionRefused{"This draft has no changes from the branch."}
	}
	person := map[string]string{"name": submissionAuthor, "email": submissionEmail, "date": s.At.UTC().Format(time.RFC3339)}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := p.request(ctx, http.MethodPost, p.apiPath()+"/git/commits", map[string]any{"message": s.message(), "tree": tree.SHA, "parents": []string{s.Base}, "author": person, "committer": person}, &commit); err != nil {
		return "", err
	}
	if !ValidCommit(commit.SHA) {
		return "", errors.New("provider omitted the new commit")
	}
	if err := p.request(ctx, http.MethodPost, p.apiPath()+"/git/refs", map[string]string{"ref": "refs/heads/" + s.branch(), "sha": commit.SHA}, nil); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

// createGitLabSubmission commits with the Commits API, which starts the branch from the exact base
// commit and refuses a branch that already exists.
func (p *Provider) createGitLabSubmission(ctx context.Context, s Submission, changes []fileChange) (string, error) {
	actions := []map[string]string{}
	for _, c := range changes {
		action := map[string]string{"action": "create", "file_path": c.Path}
		switch {
		case c.Delete:
			action["action"] = "delete"
		case c.Blob != "":
			action["action"] = "update"
		}
		if !c.Delete {
			action["encoding"] = "base64"
			action["content"] = base64.StdEncoding.EncodeToString([]byte(c.Content))
		}
		actions = append(actions, action)
	}
	var commit struct {
		ID string `json:"id"`
	}
	body := map[string]any{"branch": s.branch(), "start_sha": s.Base, "commit_message": s.message(), "author_name": submissionAuthor, "author_email": submissionEmail, "actions": actions}
	if err := p.request(ctx, http.MethodPost, p.apiPath()+"/repository/commits", body, &commit); err != nil {
		return "", err
	}
	if !ValidCommit(commit.ID) {
		return "", errors.New("provider omitted the new commit")
	}
	return commit.ID, nil
}

// createGiteaSubmission starts the branch at the exact base commit, which Gitea refuses when the
// branch exists, then changes its files. Each update and removal names the blob it replaces.
func (p *Provider) createGiteaSubmission(ctx context.Context, s Submission, changes []fileChange, branchExists bool) (string, error) {
	if err := p.giteaSupportsSubmissions(ctx); err != nil {
		return "", err
	}
	if !branchExists {
		if err := p.request(ctx, http.MethodPost, p.apiPath()+"/branches", map[string]string{"new_branch_name": s.branch(), "old_ref_name": s.Base}, nil); err != nil {
			return "", err
		}
	}
	files := []map[string]string{}
	for _, c := range changes {
		file := map[string]string{"operation": "create", "path": c.Path}
		switch {
		case c.Delete:
			file["operation"] = "delete"
		case c.Blob != "":
			file["operation"] = "update"
		}
		if c.Blob != "" {
			file["sha"] = c.Blob
		}
		if !c.Delete {
			file["content"] = base64.StdEncoding.EncodeToString([]byte(c.Content))
		}
		files = append(files, file)
	}
	person := map[string]string{"name": submissionAuthor, "email": submissionEmail}
	at := s.At.UTC().Format(time.RFC3339)
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	body := map[string]any{"branch": s.branch(), "message": s.message(), "author": person, "committer": person, "dates": map[string]string{"author": at, "committer": at}, "files": files}
	if err := p.request(ctx, http.MethodPost, p.apiPath()+"/contents", body, &out); err != nil {
		return "", err
	}
	if !ValidCommit(out.Commit.SHA) {
		return "", errors.New("provider omitted the new commit")
	}
	return out.Commit.SHA, nil
}

// giteaMinimumMinor is the first Gitea 1.x release, 1.20, that can start a branch at a commit
// (old_ref_name) and change several files in one commit (ChangeFiles). An older one starts the
// branch at the default branch instead, so it is refused before anything is written.
const giteaMinimumMinor = 20

var giteaVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)[0-9A-Za-z.+-]{0,40}$`)

func (p *Provider) giteaSupportsSubmissions(ctx context.Context) error {
	var data struct {
		Version string `json:"version"`
	}
	if err := p.request(ctx, http.MethodGet, "/api/v1/version", nil, &data); err != nil {
		return err
	}
	// A build that names no release, such as a development one, is left to the commit checks.
	parts := giteaVersion.FindStringSubmatch(data.Version)
	if parts == nil {
		return nil
	}
	major, _ := strconv.Atoi(parts[1])
	minor, _ := strconv.Atoi(parts[2])
	if major == 0 || major == 1 && minor < giteaMinimumMinor {
		return &SubmissionRefused{"Submitting a draft needs Gitea 1.20 or later, and this server runs " + data.Version + ". Upgrade Gitea, then submit again."}
	}
	return nil
}

type commitInfo struct {
	Parents []string
	Message string
	// Paths are every path the commit changes from its parent, including a renamed file's old path
	// where the provider names it.
	Paths []string
}

// verifySubmission accepts a commit only if it is exactly this submission's change on top of its
// base: the only parent, the message, no other path changed and each file's exact contents.
func (p *Provider) verifySubmission(ctx context.Context, s Submission, changes []fileChange, commit string) error {
	refused := &SubmissionRefused{"Branch " + s.branch() + " already holds a different change. Delete that branch at the Git provider, then submit again."}
	if !ValidCommit(commit) {
		return errors.New("provider omitted exact commit")
	}
	info, err := p.commitInfo(ctx, commit)
	if err != nil {
		return err
	}
	if len(info.Parents) != 1 || info.Parents[0] != s.Base || strings.TrimSpace(info.Message) != strings.TrimSpace(s.message()) {
		return refused
	}
	expected := map[string]bool{}
	for _, c := range changes {
		expected[c.Path] = true
	}
	for _, name := range info.Paths {
		if !expected[name] {
			return refused
		}
	}
	blobs, err := p.SourceBlobs(ctx, commit, slices.Sorted(maps.Keys(expected)))
	if err != nil {
		return err
	}
	for _, c := range changes {
		blob, exists := blobs[c.Path]
		if c.Delete && exists || !c.Delete && blob != GitBlobID(commit, c.Content) {
			return refused
		}
	}
	return nil
}

func (p *Provider) commitInfo(ctx context.Context, commit string) (commitInfo, error) {
	var info commitInfo
	switch p.Connection.Provider {
	case "gitlab":
		var data struct {
			ID      string   `json:"id"`
			Message string   `json:"message"`
			Parents []string `json:"parent_ids"`
		}
		if err := p.request(ctx, http.MethodGet, p.apiPath()+"/repository/commits/"+commit, nil, &data); err != nil {
			return info, err
		}
		if data.ID != commit {
			return info, errors.New("provider answered about another commit")
		}
		var diffs []struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
		}
		// A submission changes at most 32 paths, so a longer answer is not one.
		if err := p.request(ctx, http.MethodGet, p.apiPath()+"/repository/commits/"+commit+"/diff?per_page=100", nil, &diffs); err != nil {
			return info, err
		}
		info = commitInfo{Parents: data.Parents, Message: data.Message}
		for _, d := range diffs {
			info.Paths = append(info.Paths, d.OldPath, d.NewPath)
		}
	default:
		endpoint := p.apiPath() + "/commits/" + commit
		if p.Connection.Provider == "gitea" {
			endpoint = p.apiPath() + "/git/commits/" + commit + "?stat=false&verification=false&files=true"
		}
		var data struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
			Files []struct {
				Filename         string `json:"filename"`
				PreviousFilename string `json:"previous_filename"`
			} `json:"files"`
		}
		if err := p.request(ctx, http.MethodGet, endpoint, nil, &data); err != nil {
			return info, err
		}
		if data.SHA != commit {
			return info, errors.New("provider answered about another commit")
		}
		info.Message = data.Commit.Message
		for _, parent := range data.Parents {
			info.Parents = append(info.Parents, parent.SHA)
		}
		for _, f := range data.Files {
			info.Paths = append(info.Paths, f.Filename)
			if f.PreviousFilename != "" {
				info.Paths = append(info.Paths, f.PreviousFilename)
			}
		}
	}
	return info, nil
}

// CanPush asks the Git server whether the provider's credential may push to this repository over
// HTTPS, without pushing: Git asks for the receive-pack advertisement first, and GitHub, GitLab and
// Gitea refuse it unless the credential can write. Anything but a clear yes or no is an error, so
// an unknown answer is never taken as read-only.
func (p *Provider) CanPush(ctx context.Context) (bool, error) {
	base := GitURL(p.Connection)
	if p.Connection.Provider == "github" && p.base != p.Connection.BaseURL {
		base = p.base + "/" + p.Connection.Repository + ".git"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/info/refs?service=git-receive-pack", nil)
	if err != nil {
		return false, errors.New("invalid Git request")
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.access.Username+":"+p.access.Password)))
	req.Header.Set("Git-Protocol", "version=2")
	response, err := p.client.Do(req)
	if err != nil {
		return false, errors.New("Git server request failed")
	}
	response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return true, nil
	// The server authenticated the credential and refused it write access.
	case response.StatusCode == http.StatusForbidden, response.StatusCode == http.StatusNotFound:
		return false, nil
	// The server didn't accept the credential at all, so says nothing about what it could do.
	case response.StatusCode == http.StatusUnauthorized:
		return false, ErrCredentialRejected
	}
	return false, &ProviderError{Status: response.StatusCode}
}

// ErrCredentialRejected is CanPush's answer when the Git server did not accept the credential.
var ErrCredentialRejected = errors.New("the Git server did not accept the credential")
