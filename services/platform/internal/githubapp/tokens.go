package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type tokenKey struct {
	Installation, Repository int64
	Write                    bool
	PullRequests             bool
}
type cachedToken struct {
	err     error
	token   string
	expires time.Time
	ready   chan struct{}
}

// tokenReuse is the least time a token must have left to be used again.
const tokenReuse = 35 * time.Minute

// Mint returns a token for management's own calls. Tokens are cached by installation,
// repository and permissions, so they are shared across workspaces and must never leave
// management: a holder could revoke a shared token for everyone.
func (c *Client) Mint(ctx context.Context, installation, repository int64, write bool) (string, error) {
	return c.mintCached(ctx, tokenKey{installation, repository, write, false})
}
func (c *Client) MintPreview(ctx context.Context, installation, repository int64) (string, error) {
	return c.mintCached(ctx, tokenKey{installation, repository, false, true})
}

// runnerTokenLimit bounds the runner tokens one workspace holds in the cache. It is more than the
// pipelines a workspace may have unfinished, so a workspace's tokens give way only to its own.
const runnerTokenLimit = 64

// runnerKey is one pipeline's runner token.
type runnerKey struct {
	Pipeline                 uuid.UUID
	Installation, Repository int64
}
type runnerToken struct {
	token   string
	expires time.Time
}

// MintRunnerClone returns the token for one pipeline's runner: read-only contents of the one
// repository, valid until at least until, and when it expires. It is the pipeline's own, never
// management's or another pipeline's, so a runner that revokes it affects only itself. The
// pipeline's retries and redeliveries get the same token while it lasts, so a runner cannot
// make management mint more by asking again. Tokens are held apart by workspace, so one
// workspace's pipelines never displace another's.
func (c *Client) MintRunnerClone(ctx context.Context, workspace, pipeline uuid.UUID, installation, repository int64, until time.Time) (string, time.Time, error) {
	key := runnerKey{pipeline, installation, repository}
	c.mu.Lock()
	if v, ok := c.runners[workspace][key]; ok && !v.expires.Before(until) {
		c.mu.Unlock()
		return v.token, v.expires, nil
	}
	c.mu.Unlock()
	token, expires, err := c.mint(ctx, installation, repository, false, false)
	if err != nil {
		return "", time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneRunnerTokens()
	tokens := c.runners[workspace]
	if tokens == nil {
		tokens = map[runnerKey]runnerToken{}
		c.runners[workspace] = tokens
	}
	if _, held := tokens[key]; !held && len(tokens) >= runnerTokenLimit {
		var soonest runnerKey
		for k, v := range tokens {
			if old, ok := tokens[soonest]; !ok || v.expires.Before(old.expires) {
				soonest = k
			}
		}
		delete(tokens, soonest)
	}
	tokens[key] = runnerToken{token, expires}
	return token, expires, nil
}

// pruneRunnerTokens drops expired runner tokens, at most once a minute.
func (c *Client) pruneRunnerTokens() {
	now := time.Now()
	if now.Before(c.runnersPruned.Add(time.Minute)) {
		return
	}
	c.runnersPruned = now
	for workspace, tokens := range c.runners {
		for k, v := range tokens {
			if !v.expires.After(now) {
				delete(tokens, k)
			}
		}
		if len(tokens) == 0 {
			delete(c.runners, workspace)
		}
	}
}

// Forget drops token from the cache, as when GitHub has rejected it, so the next call mints a
// new one. A newer token cached under the same key is kept.
func (c *Client) Forget(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.tokens {
		if v.ready == nil && v.token == token {
			delete(c.tokens, k)
		}
	}
}

// ErrTokenRejected is Validate's error when GitHub did not accept the cached installation token,
// which it has dropped, so the next call mints another.
var ErrTokenRejected = errors.New("GitHub did not accept the installation token")

// Unauthorized reports whether GitHub rejected the credential of a request.
func Unauthorized(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusUnauthorized
}

// mintCached returns a token for key with tokenReuse left, from the cache when it holds one,
// and otherwise freshly minted.
func (c *Client) mintCached(ctx context.Context, key tokenKey) (string, error) {
	c.mu.Lock()
	if v := c.tokens[key]; v != nil {
		if v.ready != nil {
			ready := v.ready
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-ready:
				return v.token, v.err
			}
		}
		if time.Until(v.expires) >= tokenReuse {
			token := v.token
			c.mu.Unlock()
			return token, nil
		}
		delete(c.tokens, key)
	}
	for k, v := range c.tokens {
		if v.ready == nil && time.Until(v.expires) < tokenReuse {
			delete(c.tokens, k)
		}
	}
	if len(c.tokens) >= 512 {
		for k, v := range c.tokens {
			if v.ready == nil {
				delete(c.tokens, k)
				break
			}
		}
	}
	if len(c.tokens) >= 512 {
		c.mu.Unlock()
		return "", errors.New("GitHub token requests are at capacity")
	}
	pending := &cachedToken{ready: make(chan struct{})}
	c.tokens[key] = pending
	c.mu.Unlock()
	token, expires, err := c.mint(ctx, key.Installation, key.Repository, key.Write, key.PullRequests)
	c.mu.Lock()
	pending.err = err
	ready := pending.ready
	if err != nil {
		delete(c.tokens, key)
	} else {
		pending.token = token
		pending.expires = expires
		pending.ready = nil
	}
	close(ready)
	c.mu.Unlock()
	return token, err
}
func (c *Client) mint(ctx context.Context, installation, repository int64, write, requests bool) (string, time.Time, error) {
	permissions := map[string]string{"contents": "read"}
	if requests {
		permissions["pull_requests"] = "read"
	}
	if write {
		permissions["contents"] = "write"
		permissions["pull_requests"] = "write"
	}
	var out struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []Repository      `json:"repositories"`
	}
	e := c.appRequest(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation), map[string]any{"repository_ids": []int64{repository}, "permissions": permissions}, &out)
	if e != nil {
		return "", time.Time{}, e
	}
	if !validToken(out.Token) || time.Until(out.ExpiresAt) < 35*time.Minute || len(out.Repositories) != 1 || out.Repositories[0].ID != repository {
		return "", time.Time{}, errors.New("GitHub token scope or lifetime was not confirmed")
	}
	for k, v := range out.Permissions {
		if k == "metadata" && v == "read" {
			continue
		}
		if permissions[k] != v {
			return "", time.Time{}, errors.New("GitHub returned broader token permissions")
		}
	}
	for k, v := range permissions {
		if out.Permissions[k] != v {
			return "", time.Time{}, errors.New("GitHub token permission is missing")
		}
	}
	return out.Token, out.ExpiresAt, nil
}
