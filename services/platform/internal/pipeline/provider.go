package pipeline

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/v2"
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

var errProviderResponseSize = errors.New("provider response exceeds its limit")

// ErrBranchNotFound is Resolve's error when the repository answers and has no such branch.
var ErrBranchNotFound = errors.New("branch not found")

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
	if err != nil {
		return errors.New("provider request failed")
	}
	if len(raw) > 1<<20 {
		return errProviderResponseSize
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
		Owner      struct {
			Visibility string `json:"visibility"`
		} `json:"owner"`
	}
	if err := p.request(ctx, "GET", p.apiPath(), nil, &data); err != nil {
		return 0, false, err
	}
	if data.ID < 1 {
		return 0, false, errors.New("repository identity is missing")
	}
	private := data.Private || data.Visibility == "private" || data.Visibility == "internal"
	// Gitea answers private: false for a repository whose owner only signed-in users or members
	// can see, and nobody can clone it anonymously.
	if p.Connection.Provider == "gitea" && data.Owner.Visibility != "public" {
		private = true
	}
	return data.ID, private, nil
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
		var status *ProviderError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return "", fmt.Errorf("%w: %w", ErrBranchNotFound, err)
		}
		return "", err
	}
	// GitLab and Gitea name a branch's commit id, and GitHub names it sha.
	commit := data.Commit.ID
	if p.Connection.Provider == "github" {
		commit = data.Commit.SHA
	}
	if !ValidCommit(commit) {
		return "", errors.New("provider omitted exact commit")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return "", err
	}
	return commit, nil
}

type Push struct{ Delivery, Branch, Commit string }

func verifyDelivery(kind, secret string, repositoryID int64, headers http.Header, body []byte) (string, string, error) {
	invalid := errors.New("invalid push delivery")
	if len(secret) < 32 || repositoryID < 1 || len(body) > 1<<20 {
		return "", "", invalid
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
			return "", "", invalid
		}
		actual, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if err != nil || !hmac.Equal(actual, mac.Sum(nil)) {
			return "", "", invalid
		}
		event = headers.Get(header)
		delivery = headers.Get(id)
	case "gitlab":
		actual, expected := sha256.Sum256([]byte(headers.Get("X-Gitlab-Token"))), sha256.Sum256([]byte(secret))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			return "", "", invalid
		}
		event = headers.Get("X-Gitlab-Event")
		delivery = headers.Get("webhook-id")
		if delivery == "" {
			delivery = headers.Get("Idempotency-Key")
		}
		if delivery == "" {
			delivery = headers.Get("X-Gitlab-Event-UUID")
		}
	default:
		return "", "", invalid
	}
	if delivery == "" || len(delivery) > 200 || strings.ContainsAny(delivery, "\r\n\x00") {
		return "", "", invalid
	}
	return event, delivery, nil
}

func VerifyPush(kind, secret string, repositoryID int64, headers http.Header, body []byte) (Push, error) {
	event, delivery, err := verifyDelivery(kind, secret, repositoryID, headers, body)
	invalid := errors.New("invalid push delivery")
	if err != nil || (kind == "gitlab" && event != "Push Hook") || (kind != "gitlab" && event != "push") {
		return Push{}, invalid
	}
	var data struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
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
		Merged          bool   `json:"merged"`
		State           string `json:"state"`
		MergeCommitSHA  string `json:"merge_commit_sha"`
		SquashCommitSHA string `json:"squash_commit_sha"`
		SHA             string `json:"sha"`
	}
	if err := p.request(ctx, "GET", endpoint, nil, &r); err != nil {
		return "", err
	}
	if !r.Merged && r.State != "merged" {
		return "", nil
	}
	commit := r.MergeCommitSHA
	// A GitLab project that merges by fast-forward makes no merge commit, so the branch ends on
	// the squashed commit, or else on the request's own head.
	if p.Connection.Provider == "gitlab" && commit == "" {
		commit = r.SquashCommitSHA
		if commit == "" {
			commit = r.SHA
		}
	}
	if !ValidCommit(commit) {
		return "", errors.New("provider omitted merge commit")
	}
	if err := p.verifyRepository(ctx); err != nil {
		return "", err
	}
	return commit, nil
}
