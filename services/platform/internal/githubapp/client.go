// Package githubapp owns the GitHub.com App authentication boundary.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/outbound"
)

type Config struct {
	ID                                                                      int64
	ClientID, Slug, ClientSecret, WebhookSecret, PrivateKey, PrivateKeyFile string
	// Fixture origins are rejected outside loopback development.
	APIOrigin, WebOrigin string
}

func Load() Config {
	rawID := os.Getenv("ASTILBA_GITHUB_APP_ID")
	id, _ := strconv.ParseInt(rawID, 10, 64)
	if rawID != "" && id < 1 {
		id = -1
	}
	return Config{ID: id, ClientID: os.Getenv("ASTILBA_GITHUB_APP_CLIENT_ID"), Slug: os.Getenv("ASTILBA_GITHUB_APP_SLUG"), ClientSecret: os.Getenv("ASTILBA_GITHUB_APP_CLIENT_SECRET"), WebhookSecret: os.Getenv("ASTILBA_GITHUB_APP_WEBHOOK_SECRET"), PrivateKey: os.Getenv("ASTILBA_GITHUB_APP_PRIVATE_KEY"), PrivateKeyFile: os.Getenv("ASTILBA_GITHUB_APP_PRIVATE_KEY_FILE"), APIOrigin: os.Getenv("ASTILBA_GITHUB_APP_FIXTURE_API"), WebOrigin: os.Getenv("ASTILBA_GITHUB_APP_FIXTURE_WEB")}
}

type Client struct {
	validations        map[authorityKey]*cachedAuthority
	Config             Config
	api, web, callback string
	key                *rsa.PrivateKey
	http               *http.Client
	mu                 sync.Mutex
	tokens             map[tokenKey]*cachedToken
	// paused is when GitHub said the App may make requests again, after limiting them.
	paused time.Time
}

func New(c Config, origin string, development bool) (*Client, error) {
	if c == (Config{}) {
		return nil, nil
	}
	if c.ID < 1 || c.ClientID == "" || c.Slug == "" || len(c.ClientSecret) < 16 || len(c.WebhookSecret) < 32 || (c.PrivateKey == "") == (c.PrivateKeyFile == "") {
		return nil, errors.New("GitHub App configuration is incomplete")
	}
	for _, v := range []string{c.ClientID, c.Slug} {
		if len(v) > 100 || strings.ContainsAny(v, " /?&#\r\n\\") {
			return nil, errors.New("invalid GitHub App identity")
		}
	}
	raw := []byte(c.PrivateKey)
	if c.PrivateKeyFile != "" {
		f, e := os.Open(c.PrivateKeyFile)
		if e != nil {
			return nil, errors.New("cannot open GitHub App private key")
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
			return nil, errors.New("GitHub App key must be a bounded owner-only regular file")
		}
		raw, e = io.ReadAll(io.LimitReader(f, 16385))
		if e != nil {
			return nil, e
		}
	}
	if len(raw) > 16384 {
		return nil, errors.New("GitHub App key exceeds limit")
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid GitHub App PEM")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else if v, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
		key, _ = v.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("GitHub App requires an RSA private key of at least 2048 bits")
	}
	api, web := "https://api.github.com", "https://github.com"
	if c.APIOrigin != "" || c.WebOrigin != "" {
		for _, v := range []string{c.APIOrigin, c.WebOrigin} {
			u, e := url.Parse(v)
			if !development || e != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !(u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()) {
				return nil, errors.New("GitHub App fixtures require exact loopback development origins")
			}
		}
		api, web = c.APIOrigin, c.WebOrigin
	}
	// The PEM is not retained in the general configuration value.
	c.PrivateKey = ""
	c.PrivateKeyFile = ""
	return &Client{Config: c, api: api, web: web, callback: origin + "/github/callback", key: key, http: outbound.Client(development, 20*time.Second), tokens: make(map[tokenKey]*cachedToken), validations: make(map[authorityKey]*cachedAuthority)}, nil
}
func (c *Client) FixtureAPI() string {
	if c.api != "https://api.github.com" {
		return c.api
	}
	return ""
}
func (c *Client) InstallURL() string { return c.web + "/apps/" + c.Config.Slug + "/installations/new" }
func (c *Client) AuthorizeURL(state, verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {c.Config.ClientID}, "redirect_uri": {c.callback}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(h[:])}, "code_challenge_method": {"S256"}}
	return c.web + "/login/oauth/authorize?" + q.Encode()
}
func (c *Client) jwt() (string, error) {
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(8 * time.Minute).Unix(), "iss": c.Config.ClientID})
	value := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	h := sha256.Sum256([]byte(value))
	sig, e := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, h[:])
	if e != nil {
		return "", e
	}
	return value + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

type APIError struct{ Status int }

func (e *APIError) Error() string { return fmt.Sprintf("GitHub returned HTTP %d", e.Status) }

var errRevoked = errors.New("GitHub installation authority was removed")

// RateLimited is GitHub refusing a request under its rate limits, until Until.
type RateLimited struct{ Until time.Time }

func (e *RateLimited) Error() string {
	return "GitHub is limiting requests until " + e.Until.UTC().Format(time.RFC3339)
}

// Limited reports whether err is GitHub limiting requests, and until when.
func Limited(err error) (time.Time, bool) {
	var limited *RateLimited
	if errors.As(err, &limited) {
		return limited.Until, true
	}
	return time.Time{}, false
}

// rateLimit reads when GitHub allows requests again from a refusal under its rate limits,
// following GitHub's guidance: retry-after when present, then the primary limit's reset, and
// otherwise a minute. A 403 without those signals is a rate limit only if its body says so.
func rateLimit(res *http.Response, now time.Time) (time.Time, bool) {
	if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	until := now.Add(time.Minute)
	if seconds, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && seconds > 0 {
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if reset, e := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); e == nil && res.Header.Get("X-RateLimit-Remaining") == "0" {
		until = time.Unix(reset, 0)
	} else if res.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if !strings.Contains(strings.ToLower(string(body)), "rate limit") {
			return time.Time{}, false
		}
	}
	// GitHub's limits reset within the hour, so a later time is a header to distrust.
	if until.Before(now.Add(time.Second)) {
		until = now.Add(time.Second)
	}
	if until.After(now.Add(time.Hour)) {
		until = now.Add(time.Hour)
	}
	return until, true
}

// appRequest makes a request as the App itself. Once GitHub limits one, the others wait out the
// same time without asking, since asking again while limited can get an App blocked.
func (c *Client) appRequest(ctx context.Context, method, path string, body, out any) error {
	c.mu.Lock()
	paused := c.paused
	c.mu.Unlock()
	if time.Now().Before(paused) {
		return &RateLimited{Until: paused}
	}
	jwt, e := c.jwt()
	if e != nil {
		return e
	}
	e = c.request(ctx, method, c.api, path, jwt, body, out)
	if until, limited := Limited(e); limited {
		c.mu.Lock()
		if until.After(c.paused) {
			c.paused = until
		}
		c.mu.Unlock()
	}
	return e
}

func Revoked(err error) bool { return errors.Is(err, errRevoked) }
func (c *Client) request(ctx context.Context, method, origin, path, token string, body any, out any) error {
	var reader io.Reader
	content := "application/json"
	if form, ok := body.(url.Values); ok {
		reader = strings.NewReader(form.Encode())
		content = "application/x-www-form-urlencoded"
	} else if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, origin+path, reader)
	if e != nil {
		return errors.New("invalid GitHub request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", content)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := c.http.Do(req)
	if e != nil {
		return errors.New("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if until, limited := rateLimit(res, time.Now()); limited {
			return &RateLimited{Until: until}
		}
		return &APIError{res.StatusCode}
	}
	if out == nil {
		return nil
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return errors.New("GitHub response exceeds limit")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}
func validToken(s string) bool {
	return s != "" && len(s) <= 8192 && !strings.ContainsAny(s, "\x00\r\n")
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (c *Client) Exchange(ctx context.Context, code, verifier string) (string, User, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	err := c.request(ctx, "POST", c.web, "/login/oauth/access_token", "", url.Values{"client_id": {c.Config.ClientID}, "client_secret": {c.Config.ClientSecret}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {c.callback}}, &out)
	if err != nil {
		return "", User{}, err
	}
	if out.Error != "" || !validToken(out.AccessToken) {
		return "", User{}, errors.New("GitHub authorization was not granted")
	}
	var user User
	err = c.request(ctx, "GET", c.api, "/user", out.AccessToken, nil, &user)
	if err == nil && (user.ID < 1 || user.Login == "" || len(user.Login) > 100) {
		err = errors.New("GitHub user identity is missing")
	}
	return out.AccessToken, user, err
}
func (c *Client) RevokeUser(ctx context.Context, token string) error {
	// This endpoint requires client credentials through HTTP Basic, not an App JWT.
	raw, _ := json.Marshal(map[string]string{"access_token": token})
	req, e := http.NewRequestWithContext(ctx, "DELETE", c.api+"/applications/"+c.Config.ClientID+"/token", bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.SetBasicAuth(c.Config.ClientID, c.Config.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, e := c.http.Do(req)
	if e != nil {
		return errors.New("GitHub revocation failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 204 && res.StatusCode != 404 {
		return &APIError{res.StatusCode}
	}
	return nil
}

type Installation struct {
	ID          int64             `json:"id"`
	AppID       int64             `json:"app_id"`
	SuspendedAt *time.Time        `json:"suspended_at"`
	Permissions map[string]string `json:"permissions"`
	Account     struct {
		Login string `json:"login"`
	} `json:"account"`
}
type Repository struct {
	ID          int64  `json:"id"`
	FullName    string `json:"full_name"`
	Private     bool   `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func (c *Client) Installations(ctx context.Context, token string, page int) ([]Installation, error) {
	var out struct {
		Installations []Installation `json:"installations"`
	}
	e := c.request(ctx, "GET", c.api, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), token, nil, &out)
	if len(out.Installations) > 100 {
		return nil, errors.New("GitHub installation page exceeds limit")
	}
	return out.Installations, e
}
func (c *Client) Repositories(ctx context.Context, token string, id int64, page int) ([]Repository, error) {
	var out struct {
		Repositories []Repository `json:"repositories"`
	}
	e := c.request(ctx, "GET", c.api, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", id, page), token, nil, &out)
	if len(out.Repositories) > 100 {
		return nil, errors.New("GitHub repository page exceeds limit")
	}
	return out.Repositories, e
}
func (c *Client) UserRepository(ctx context.Context, token, name string) (Repository, error) {
	var out Repository
	e := c.request(ctx, "GET", c.api, "/repos/"+name, token, nil, &out)
	return out, e
}
func (c *Client) Authority(ctx context.Context, installation int64, repository string) error {
	var app struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
	}
	if e := c.appRequest(ctx, "GET", "/app", nil, &app); e != nil {
		return e
	}
	if app.ID != c.Config.ID || app.ClientID != c.Config.ClientID {
		return errors.New("GitHub App identity changed")
	}
	var i Installation
	if e := c.appRequest(ctx, "GET", "/repos/"+repository+"/installation", nil, &i); e != nil {
		var api *APIError
		if errors.As(e, &api) && api.Status == 404 {
			return errRevoked
		}
		return e
	}
	if i.ID != installation || i.AppID != c.Config.ID || i.SuspendedAt != nil || i.Permissions["contents"] != "write" || i.Permissions["pull_requests"] != "write" {
		return errRevoked
	}
	return nil
}

type authorityKey struct {
	Installation, Repository, Generation int64
	Name                                 string
	Private                              bool
}
type cachedAuthority struct {
	err   error
	until time.Time
	ready chan struct{}
}

// Validate reuses only a successful, exact identity check for five seconds.
// Every consumer must still establish its controller-local active binding.
func (c *Client) Validate(ctx context.Context, installation, repository, generation int64, name string, private bool) error {
	key := authorityKey{installation, repository, generation, name, private}
	c.mu.Lock()
	if v := c.validations[key]; v != nil {
		if v.ready != nil {
			ready := v.ready
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ready:
				return v.err
			}
		}
		if time.Now().Before(v.until) {
			c.mu.Unlock()
			return nil
		}
		delete(c.validations, key)
	}
	for k, v := range c.validations {
		if v.ready == nil && !time.Now().Before(v.until) {
			delete(c.validations, k)
		}
	}
	if len(c.validations) >= 512 {
		for k, v := range c.validations {
			if v.ready == nil {
				delete(c.validations, k)
				break
			}
		}
	}
	if len(c.validations) >= 512 {
		c.mu.Unlock()
		return errors.New("GitHub authority checks are at capacity")
	}
	pending := &cachedAuthority{ready: make(chan struct{})}
	c.validations[key] = pending
	c.mu.Unlock()
	err := c.Authority(ctx, installation, name)
	if err == nil {
		var token string
		token, err = c.Mint(ctx, installation, repository, false)
		if err == nil {
			var repo Repository
			err = c.request(ctx, "GET", c.api, "/repos/"+name, token, nil, &repo)
			if Unauthorized(err) {
				c.Forget(token)
				err = fmt.Errorf("%w: %w", ErrTokenRejected, err)
			}
			if err == nil && (repo.ID != repository || repo.FullName != name || repo.Private != private) {
				err = errors.New("GitHub repository identity or visibility changed")
			}
		}
	}
	c.mu.Lock()
	pending.err = err
	ready := pending.ready
	if err != nil {
		delete(c.validations, key)
	} else {
		pending.until = time.Now().Add(5 * time.Second)
		pending.ready = nil
	}
	close(ready)
	c.mu.Unlock()
	return err
}

func (c *Client) VerifyWebhook(headers http.Header, body []byte) error {
	signature := headers.Get("X-Hub-Signature-256")
	raw, e := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	mac := hmac.New(sha256.New, []byte(c.Config.WebhookSecret))
	_, _ = mac.Write(body)
	if len(body) > 150000 || !strings.HasPrefix(signature, "sha256=") || e != nil || !hmac.Equal(raw, mac.Sum(nil)) {
		return errors.New("invalid GitHub webhook signature")
	}
	return nil
}
