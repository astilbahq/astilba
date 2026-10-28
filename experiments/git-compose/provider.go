// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type apiError struct {
	kind                string
	status              int
	sourceBranchMissing bool
}

func (e *apiError) Error() string { return fmt.Sprintf("%s API returned HTTP %d", e.kind, e.status) }

type Provider struct {
	kind, login, token string
	base               *url.URL
	client             *http.Client
}

func NewProvider(kind, base, login, token string) (*Provider, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" || login == "" {
		return nil, errors.New("invalid provider configuration")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("HTTPS required except for loopback test servers")
	}
	if kind != "github" && kind != "gitlab" && kind != "gitea" {
		return nil, errors.New("unknown provider")
	}
	if kind == "github" && (u.Host != "api.github.com" || u.Path != "") {
		return nil, errors.New("this spike tests hosted GitHub only")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &Provider{kind: kind, base: u, login: login, token: token, client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("provider redirects are not followed")
		},
	}}, nil
}

func validRepository(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return false
			}
		}
	}
	return true
}

func (p *Provider) gitURL(repo string) string {
	base := p.base.String()
	if p.kind == "github" {
		base = "https://github.com"
	}
	return base + "/" + repo + ".git"
}

func (p *Provider) apiPath(repo string) string {
	switch p.kind {
	case "github":
		return "/repos/" + repo
	case "gitlab":
		return "/api/v4/projects/" + url.PathEscape(repo)
	default:
		return "/api/v1/repos/" + repo
	}
}

func (p *Provider) request(ctx context.Context, method, path string, body, out any) error {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode provider request")
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base.String()+path, input)
	if err != nil {
		return errors.New("invalid provider request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	switch p.kind {
	case "github":
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	case "gitlab":
		req.Header.Set("PRIVATE-TOKEN", p.token)
	case "gitea":
		req.Header.Set("Authorization", "token "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Transport errors can contain a private URL; retain only the operation.
		return fmt.Errorf("%s API transport failed", p.kind)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rejected := &apiError{kind: p.kind, status: resp.StatusCode}
		if p.kind == "gitlab" && method == "POST" && strings.HasSuffix(path, "/merge_requests") && resp.StatusCode == http.StatusBadRequest {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
			var compact bytes.Buffer
			rejected.sourceBranchMissing = readErr == nil && len(data) <= 4096 && json.Compact(&compact, data) == nil && compact.String() == `{"message":{"source_branch":["does not exist"]}}`
		}
		return rejected
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return errors.New("provider response exceeds the fixture limit")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid provider response")
	}
	return nil
}

func (p *Provider) RepositoryID(ctx context.Context, repo string) (int64, error) {
	if !validRepository(repo) || p.kind != "gitlab" && strings.Count(repo, "/") != 1 {
		return 0, errors.New("invalid repository path")
	}
	var result struct{ ID int64 }
	if err := p.request(ctx, "GET", p.apiPath(repo), nil, &result); err != nil {
		return 0, err
	}
	if result.ID <= 0 {
		return 0, errors.New("provider omitted repository identity")
	}
	return result.ID, nil
}

type Review struct {
	Number int
	URL    string
}

func (p *Provider) openReview(ctx context.Context, repo, base, branch string) (Review, error) {
	path := p.apiPath(repo) + "/pulls"
	request := map[string]any{"title": "Update Compose images", "body": "Update the explicitly selected image values while preserving the Compose source.", "base": base, "head": branch}
	if p.kind == "gitlab" {
		path = p.apiPath(repo) + "/merge_requests"
		request = map[string]any{"title": "Update Compose images", "description": "Update the explicitly selected image values while preserving the Compose source.", "target_branch": base, "source_branch": branch}
	}
	var result struct {
		Number  int    `json:"number"`
		IID     int    `json:"iid"`
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
	}
	if err := p.request(ctx, "POST", path, request, &result); err != nil {
		return Review{}, err
	}
	if p.kind == "gitlab" {
		return Review{Number: result.IID, URL: result.WebURL}, nil
	}
	return Review{Number: result.Number, URL: result.HTMLURL}, nil
}

// ReviewHead reads until the provider has computed the review's source commit.
func (p *Provider) ReviewHead(ctx context.Context, repo string, review Review) (string, error) {
	path := fmt.Sprintf("%s/pulls/%d", p.apiPath(repo), review.Number)
	if p.kind == "gitlab" {
		path = fmt.Sprintf("%s/merge_requests/%d", p.apiPath(repo), review.Number)
	}
	for attempt := 0; attempt < 30; attempt++ {
		var result struct {
			SHA  string               `json:"sha"`
			Head struct{ SHA string } `json:"head"`
		}
		if err := p.request(ctx, "GET", path, nil, &result); err != nil {
			return "", err
		}
		sha := result.Head.SHA
		if p.kind == "gitlab" {
			sha = result.SHA
		}
		if validCommit(sha) {
			return sha, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", errors.New("provider did not populate the review head")
}
