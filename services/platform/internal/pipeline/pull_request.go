package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// PullRequest contains provider-verified identities, never a credential destination.
type PullRequest struct {
	Number         int    `json:"number"`
	Open           bool   `json:"open"`
	BaseBranch     string `json:"baseBranch"`
	Head           string `json:"head"`
	HeadRepository int64  `json:"headRepository"`
	Fork           bool   `json:"fork"`
}

func PullRequestRef(provider string, number int) string {
	if provider == "gitlab" {
		return fmt.Sprintf("refs/merge-requests/%d/head", number)
	}
	return fmt.Sprintf("refs/pull/%d/head", number)
}

func (p *Provider) pullPath() string {
	if p.Connection.Provider == "gitlab" {
		return p.apiPath() + "/merge_requests"
	}
	return p.apiPath() + "/pulls"
}

func (p *Provider) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	out := PullRequest{Number: number}
	if number < 1 || number > 1_000_000_000 {
		return out, errors.New("invalid request number")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return out, err
	}
	var r struct {
		Number        int    `json:"number"`
		IID           int    `json:"iid"`
		State         string `json:"state"`
		SHA           string `json:"sha"`
		TargetProject int64  `json:"target_project_id"`
		SourceProject int64  `json:"source_project_id"`
		TargetBranch  string `json:"target_branch"`
		Base          struct {
			Ref  string `json:"ref"`
			Repo *struct {
				ID int64 `json:"id"`
			} `json:"repo"`
		} `json:"base"`
		Head struct {
			SHA  string `json:"sha"`
			Repo *struct {
				ID int64 `json:"id"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := p.request(ctx, "GET", p.pullPath()+"/"+strconv.Itoa(number), nil, &r); err != nil {
		return out, err
	}
	base := int64(0)
	got := r.Number
	out.BaseBranch = r.Base.Ref
	out.Head = r.Head.SHA
	if r.Base.Repo != nil {
		base = r.Base.Repo.ID
	}
	if r.Head.Repo != nil {
		out.HeadRepository = r.Head.Repo.ID
	}
	if p.Connection.Provider == "gitlab" {
		got = r.IID
		base = r.TargetProject
		out.BaseBranch = r.TargetBranch
		out.Head = r.SHA
		out.HeadRepository = r.SourceProject
	}
	if got != number || base != p.Connection.RepositoryID || !ValidBranch(out.BaseBranch) {
		return out, errors.New("request repository identity mismatch")
	}
	switch r.State {
	case "open", "opened":
		out.Open = true
	case "closed", "merged":
	default:
		return out, errors.New("request state is unavailable")
	}
	if out.Open && (!ValidCommit(out.Head) || out.HeadRepository < 1) {
		return out, errors.New("request source identity is unavailable")
	}
	out.Fork = out.HeadRepository != base
	return out, nil
}

// OpenPullRequests is a bounded discovery page. Admission rereads each request.
func (p *Provider) OpenPullRequests(ctx context.Context, branch string, page int) ([]int, error) {
	if !ValidBranch(branch) || page < 1 || page > 10000 {
		return nil, errors.New("invalid discovery cursor")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return nil, err
	}
	query := url.Values{"state": {"open"}, "base": {branch}, "sort": {"created"}, "direction": {"asc"}, "per_page": {"50"}, "limit": {"50"}, "page": {strconv.Itoa(page)}}
	if p.Connection.Provider == "gitlab" {
		query = url.Values{"state": {"opened"}, "target_branch": {branch}, "order_by": {"created_at"}, "sort": {"asc"}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}
	}
	var rows []struct {
		Number int `json:"number"`
		IID    int `json:"iid"`
	}
	if err := p.request(ctx, "GET", p.pullPath()+"?"+query.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) > 50 {
		return nil, errors.New("discovery page exceeds its limit")
	}
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		n := r.Number
		if p.Connection.Provider == "gitlab" {
			n = r.IID
		}
		if n < 1 || n > 1_000_000_000 {
			return nil, errors.New("invalid discovery request")
		}
		out = append(out, n)
	}
	return out, nil
}

type PullRequestEvent struct {
	Delivery string
	Number   int
}

func VerifyPullRequest(kind, secret string, repositoryID int64, headers http.Header, body []byte) (PullRequestEvent, error) {
	event, delivery, err := verifyDelivery(kind, secret, repositoryID, headers, body)
	if err != nil {
		return PullRequestEvent{}, err
	}
	if kind == "github" && event != "pull_request" || kind == "gitea" && event != "pull_request" && event != "pull_request_sync" || kind == "gitlab" && event != "Merge Request Hook" {
		return PullRequestEvent{}, errors.New("invalid request event")
	}
	var data struct {
		Number     int `json:"number"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
		Object struct {
			IID           int   `json:"iid"`
			TargetProject int64 `json:"target_project_id"`
		} `json:"object_attributes"`
	}
	if json.Unmarshal(body, &data) != nil {
		return PullRequestEvent{}, errors.New("invalid request event")
	}
	id, number := data.Repository.ID, data.Number
	if kind == "gitlab" {
		id = data.Project.ID
		number = data.Object.IID
		if data.Object.TargetProject != id {
			return PullRequestEvent{}, errors.New("request target mismatch")
		}
	}
	if id != repositoryID || number < 1 || number > 1_000_000_000 {
		return PullRequestEvent{}, errors.New("request repository mismatch")
	}
	return PullRequestEvent{Delivery: delivery, Number: number}, nil
}
