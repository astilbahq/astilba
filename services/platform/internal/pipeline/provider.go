package pipeline

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/outbound"
)

type Policy struct {
	Origins       []string
	Development   bool
	GitHubFixture string
}

func (p Policy) Allowed(base string) (bool, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(base, "\r\n\\") || strings.HasSuffix(base, "/") {
		return false, errors.New("use an exact provider base URL")
	}
	origin := u.Scheme + "://" + u.Host
	explicit := false
	for _, allowed := range p.Origins {
		if origin == allowed {
			explicit = true
		}
	}
	public := origin == "https://api.github.com" || origin == "https://gitlab.com" || origin == "https://gitea.com"
	loop := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(p.Development && u.Scheme == "http" && loop != nil && loop.IsLoopback()) {
		return false, errors.New("provider HTTPS is required")
	}
	if !public && !explicit && !(p.Development && loop != nil && loop.IsLoopback()) {
		return false, errors.New("provider origin must be configured by the operator")
	}
	return explicit || p.Development, nil
}

type Provider struct {
	Connection Connection
	access     Credential
	client     *http.Client
	base       string
}

func NewProvider(c Connection, credential Credential, policy Policy) (*Provider, error) {
	private, err := policy.Allowed(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if !ValidRepository(c.Repository) || (c.Provider != "gitlab" && strings.Count(c.Repository, "/") != 1) || (c.Provider != "github" && c.Provider != "gitlab" && c.Provider != "gitea") {
		return nil, errors.New("invalid provider repository")
	}
	if c.Provider == "github" && c.BaseURL != "https://api.github.com" {
		return nil, errors.New("GitHub uses https://api.github.com")
	}
	for _, v := range []string{credential.Username, credential.Password} {
		if len(v) > 8192 || strings.ContainsAny(v, "\r\n\x00") {
			return nil, errors.New("invalid source credential")
		}
	}
	base := c.BaseURL
	if c.Provider == "github" && policy.GitHubFixture != "" {
		u, e := url.Parse(policy.GitHubFixture)
		if !policy.Development || e != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("GitHub fixture requires loopback development")
		}
		base = policy.GitHubFixture
		private = true
	}
	return &Provider{Connection: c, access: credential, client: outbound.Client(private, 20*time.Second), base: base}, nil
}
func GitURL(c Connection) string {
	base := c.BaseURL
	if c.Provider == "github" {
		base = "https://github.com"
	}
	return base + "/" + c.Repository + ".git"
}
func (p *Provider) apiPath() string {
	switch p.Connection.Provider {
	case "github":
		return "/repos/" + p.Connection.Repository
	case "gitlab":
		return "/api/v4/projects/" + url.PathEscape(p.Connection.Repository)
	default:
		return "/api/v1/repos/" + p.Connection.Repository
	}
}

type ProviderError struct{ Status int }

func (e *ProviderError) Error() string { return fmt.Sprintf("provider returned HTTP %d", e.Status) }
func (p *Provider) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, reader)
	if err != nil {
		return errors.New("invalid provider request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.access.Password != "" {
		switch p.Connection.Provider {
		case "github":
			req.Header.Set("Authorization", "Bearer "+p.access.Password)
		case "gitlab":
			req.Header.Set("PRIVATE-TOKEN", p.access.Password)
		default:
			req.Header.Set("Authorization", "token "+p.access.Password)
		}
	}
	if p.Connection.Provider == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return errors.New("provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ProviderError{Status: response.StatusCode}
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("provider response exceeds its limit")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("invalid provider response")
	}
	return nil
}
func (p *Provider) Repository(ctx context.Context) (int64, bool, error) {
	var data struct {
		ID         int64  `json:"id"`
		Private    bool   `json:"private"`
		Visibility string `json:"visibility"`
	}
	if err := p.request(ctx, "GET", p.apiPath(), nil, &data); err != nil {
		return 0, false, err
	}
	if data.ID < 1 {
		return 0, false, errors.New("repository identity is missing")
	}
	return data.ID, data.Private || data.Visibility == "private" || data.Visibility == "internal", nil
}
func (p *Provider) verifyRepository(ctx context.Context) error {
	id, private, err := p.Repository(ctx)
	if err != nil {
		return err
	}
	if id != p.Connection.RepositoryID || private != p.Connection.Private {
		return errors.New("repository identity or visibility changed; reconnect explicitly")
	}
	return nil
}
func (p *Provider) Resolve(ctx context.Context, branch string) (string, error) {
	if err := p.verifyRepository(ctx); err != nil {
		return "", err
	}
	if !ValidBranch(branch) {
		return "", errors.New("invalid branch")
	}
	endpoint := p.apiPath() + "/branches/" + url.PathEscape(branch)
	if p.Connection.Provider == "gitlab" {
		endpoint = p.apiPath() + "/repository/branches/" + url.PathEscape(branch)
	}
	var data struct {
		Commit struct {
			SHA string `json:"sha"`
			ID  string `json:"id"`
		} `json:"commit"`
	}
	if err := p.request(ctx, "GET", endpoint, nil, &data); err != nil {
		return "", err
	}
	commit := data.Commit.SHA
	if p.Connection.Provider == "gitlab" {
		commit = data.Commit.ID
	}
	if !ValidCommit(commit) {
		return "", errors.New("provider omitted exact commit")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return "", err
	}
	return commit, nil
}

type Review struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func (p *Provider) Review(ctx context.Context, base, branch string) (Review, error) {
	if !ValidBranch(base) || !ValidBranch(branch) {
		return Review{}, errors.New("invalid review branch")
	}
	endpoint := p.apiPath() + "/pulls"
	query := "?state=open&head=" + url.QueryEscape(strings.Split(p.Connection.Repository, "/")[0]+":"+branch)
	if p.Connection.Provider == "gitlab" {
		endpoint = p.apiPath() + "/merge_requests"
		query = "?state=opened&source_branch=" + url.QueryEscape(branch)
	}
	var existing []struct {
		Number  int    `json:"number"`
		IID     int    `json:"iid"`
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
		Head    struct {
			Ref string `json:"ref"`
		}
		Base struct {
			Ref string `json:"ref"`
		}
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	}
	if err := p.request(ctx, "GET", endpoint+query, nil, &existing); err != nil {
		return Review{}, err
	}
	for _, r := range existing {
		if p.Connection.Provider == "gitlab" && r.SourceBranch == branch && r.TargetBranch == base {
			return Review{r.IID, r.WebURL}, nil
		}
		if r.Head.Ref == branch && r.Base.Ref == base {
			return Review{r.Number, r.HTMLURL}, nil
		}
	}
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
	if err := p.request(ctx, "POST", endpoint, body, &r); err != nil {
		return Review{}, err
	}
	if p.Connection.Provider == "gitlab" {
		return Review{r.IID, r.WebURL}, nil
	}
	return Review{r.Number, r.HTMLURL}, nil
}

type Push struct{ Delivery, Branch, Commit string }

func VerifyPush(kind, secret string, repositoryID int64, headers http.Header, body []byte) (Push, error) {
	invalid := errors.New("invalid push delivery")
	if len(secret) < 32 || repositoryID < 1 || len(body) > 1<<20 {
		return Push{}, invalid
	}
	event, delivery := "", ""
	switch kind {
	case "github", "gitea":
		signature, header, id, prefix := "X-Gitea-Signature", "X-Gitea-Event", "X-Gitea-Delivery", ""
		if kind == "github" {
			signature, header, id, prefix = "X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery", "sha256="
		}
		value := headers.Get(signature)
		if !strings.HasPrefix(value, prefix) {
			return Push{}, invalid
		}
		actual, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if err != nil || !hmac.Equal(actual, mac.Sum(nil)) {
			return Push{}, invalid
		}
		event = headers.Get(header)
		delivery = headers.Get(id)
		if event != "push" {
			return Push{}, invalid
		}
	case "gitlab":
		actual, expected := sha256.Sum256([]byte(headers.Get("X-Gitlab-Token"))), sha256.Sum256([]byte(secret))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 || headers.Get("X-Gitlab-Event") != "Push Hook" {
			return Push{}, invalid
		}
		delivery = headers.Get("webhook-id")
		if delivery == "" {
			delivery = headers.Get("Idempotency-Key")
		}
		if delivery == "" {
			delivery = headers.Get("X-Gitlab-Event-UUID")
		}
	default:
		return Push{}, invalid
	}
	if delivery == "" || len(delivery) > 200 || strings.ContainsAny(delivery, "\r\n\x00") {
		return Push{}, invalid
	}
	var data struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct{ ID int64 }
		Project    struct{ ID int64 }
	}
	if json.Unmarshal(body, &data) != nil || !strings.HasPrefix(data.Ref, "refs/heads/") || !ValidBranch(strings.TrimPrefix(data.Ref, "refs/heads/")) || !ValidCommit(data.After) {
		return Push{}, invalid
	}
	id := data.Repository.ID
	if kind == "gitlab" {
		id = data.Project.ID
	}
	if id != repositoryID {
		return Push{}, invalid
	}
	return Push{Delivery: delivery, Branch: strings.TrimPrefix(data.Ref, "refs/heads/"), Commit: data.After}, nil
}

// MergedCommit returns the provider's authoritative merge identity, never a webhook's claimed SHA.
func (p *Provider) MergedCommit(ctx context.Context, number int) (string, error) {
	endpoint := p.apiPath() + "/pulls/" + strconv.Itoa(number)
	if p.Connection.Provider == "gitlab" {
		endpoint = p.apiPath() + "/merge_requests/" + strconv.Itoa(number)
	}
	var r struct {
		Merged         bool
		State          string
		MergeCommitSHA string `json:"merge_commit_sha"`
		SHA            string `json:"merge_commit_id"`
	}
	if err := p.request(ctx, "GET", endpoint, nil, &r); err != nil {
		return "", err
	}
	if !r.Merged && r.State != "merged" {
		return "", nil
	}
	commit := r.MergeCommitSHA
	if commit == "" {
		commit = r.SHA
	}
	if !ValidCommit(commit) {
		return "", errors.New("provider omitted merge commit")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return "", err
	}
	return commit, nil
}
