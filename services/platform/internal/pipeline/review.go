package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Review struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// reviewEntry is a request as the providers list and return it.
type reviewEntry struct {
	Number  int    `json:"number"`
	IID     int    `json:"iid"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	WebURL  string `json:"web_url"`
	Head    struct {
		Ref  string `json:"ref"`
		Repo *struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	SourceBranch  string `json:"source_branch"`
	TargetBranch  string `json:"target_branch"`
	SourceProject int64  `json:"source_project_id"`
	TargetProject int64  `json:"target_project_id"`
}

// ours reports whether the entry is this repository's own open request from branch into
// base. A fork can open one from a branch of the same name, and recording it would show the
// owner someone else's change as theirs.
func (p *Provider) ours(r reviewEntry, base, branch string) (Review, bool) {
	if p.Connection.Provider == "gitlab" {
		own := r.SourceBranch == branch && r.TargetBranch == base && r.SourceProject == p.Connection.RepositoryID && r.TargetProject == p.Connection.RepositoryID
		return Review{r.IID, r.WebURL}, own && r.State == "opened"
	}
	own := r.Head.Ref == branch && r.Base.Ref == base && r.Head.Repo != nil && r.Head.Repo.ID == p.Connection.RepositoryID
	return Review{r.Number, r.HTMLURL}, own && r.State == "open"
}

// reviewPages bounds the search through Gitea's open requests, which it does only after
// Gitea refused to open a second request for the branch.
const reviewPages = 20

// Review returns the open review of branch into base, opening one if there is none. GitHub
// and GitLab filter their lists by head branch. Gitea's list ignores head, so the open
// requests anyone can add would bury the review; Gitea is asked about the branch itself.
func (p *Provider) Review(ctx context.Context, base, branch string) (Review, error) {
	if !ValidBranch(base) || !ValidBranch(branch) {
		return Review{}, errors.New("invalid review branch")
	}
	if p.Connection.Provider == "gitea" {
		return p.giteaReview(ctx, base, branch)
	}
	query := url.Values{"state": {"open"}, "head": {strings.Split(p.Connection.Repository, "/")[0] + ":" + branch}, "base": {base}}
	if p.Connection.Provider == "gitlab" {
		// GitLab's filter keeps forks' requests from a branch of the same name, newest first. A
		// review is opened as soon as its branch exists, before anyone else can know the name,
		// so it comes first among the oldest.
		query = url.Values{"state": {"opened"}, "source_branch": {branch}, "target_branch": {base}, "order_by": {"created_at"}, "sort": {"asc"}, "per_page": {"100"}}
	}
	var existing []reviewEntry
	if err := p.request(ctx, "GET", p.pullPath()+"?"+query.Encode(), nil, &existing); err != nil {
		return Review{}, err
	}
	for _, r := range existing {
		if review, own := p.ours(r, base, branch); own {
			return review, nil
		}
	}
	return p.openReview(ctx, base, branch)
}

// giteaReview asks for the request from branch into base. Gitea answers with the oldest such
// request, so a closed one can hide a newer open one; Gitea then refuses to open another, and
// only that case reads through the list.
func (p *Provider) giteaReview(ctx context.Context, base, branch string) (Review, error) {
	var found reviewEntry
	err := p.request(ctx, "GET", p.pullPath()+"/"+url.PathEscape(base)+"/"+url.PathEscape(branch), nil, &found)
	var status *ProviderError
	if err != nil && !(errors.As(err, &status) && status.Status == http.StatusNotFound) {
		return Review{}, err
	}
	if review, own := p.ours(found, base, branch); err == nil && own {
		return review, nil
	}
	review, err := p.openReview(ctx, base, branch)
	if !(errors.As(err, &status) && status.Status == http.StatusConflict) {
		return review, err
	}
	// Newest first, where a retry's request is. Pages stop when one comes back empty, because
	// a server can be set to answer fewer than the limit asks for.
	query := url.Values{"state": {"open"}, "base_branch": {base}, "limit": {"50"}}
	for page := 1; page <= reviewPages; page++ {
		query.Set("page", strconv.Itoa(page))
		var existing []reviewEntry
		if err := p.request(ctx, "GET", p.pullPath()+"?"+query.Encode(), nil, &existing); err != nil {
			return Review{}, err
		}
		for _, r := range existing {
			if review, own := p.ours(r, base, branch); own {
				return review, nil
			}
		}
		if len(existing) == 0 {
			break
		}
	}
	return Review{}, errors.New("provider refused a review it does not list")
}

func (p *Provider) openReview(ctx context.Context, base, branch string) (Review, error) {
	body := map[string]any{"title": "Update deployment configuration", "body": "Update the reviewed deployment source files.", "base": base, "head": branch}
	if p.Connection.Provider == "gitlab" {
		body = map[string]any{"title": "Update deployment configuration", "description": "Update the reviewed deployment source files.", "target_branch": base, "source_branch": branch}
	}
	var r struct {
		Number  int    `json:"number"`
		IID     int    `json:"iid"`
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
	}
	if err := p.request(ctx, "POST", p.pullPath(), body, &r); err != nil {
		return Review{}, err
	}
	if p.Connection.Provider == "gitlab" {
		return Review{r.IID, r.WebURL}, nil
	}
	return Review{r.Number, r.HTMLURL}, nil
}
