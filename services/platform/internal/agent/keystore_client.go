package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// keyStoreRetry is how often an unavailable key store is opened again.
const keyStoreRetry = time.Minute

// keyStore returns this host's key store, or nil while it is unavailable. It never opens the
// store, so no request waits on a slow store or caches a failure its own deadline caused.
func (c *Client) keyStore() KeyStore {
	c.keysMu.Lock()
	defer c.keysMu.Unlock()
	return c.Keys
}

// OpenKeyStore opens this host's key store unless it is open already, and returns it, or nil while
// it is unavailable.
func (c *Client) OpenKeyStore(ctx context.Context) KeyStore {
	if keys := c.keyStore(); keys != nil || c.OpenKeys == nil {
		return keys
	}
	opened, err := c.OpenKeys(ctx)
	if err != nil {
		slog.Warn("the agent's key store is unavailable; it holds no key here until it opens", "error", err)
	}
	c.keysMu.Lock()
	defer c.keysMu.Unlock()
	if c.Keys == nil {
		c.Keys, c.KeyStoreErr = opened, err
	}
	return c.Keys
}

// keepKeyStoreOpen opens the key store, and opens it again every minute while it is unavailable,
// so a store that wasn't ready at start, such as a TPM early in boot, recovers while the agent
// carries on with its local work.
func (c *Client) keepKeyStoreOpen(ctx context.Context) {
	for c.OpenKeys != nil && c.OpenKeyStore(ctx) == nil {
		if !wait(ctx, keyStoreRetry) {
			return
		}
	}
}

// keyProtection is what this agent reports about how it protects its key, and why its store is
// unavailable if it is.
func (c *Client) keyProtection() (*api.AgentCapabilitiesKeyProtection, *string) {
	c.keysMu.Lock()
	keys, failure := c.Keys, c.KeyStoreErr
	c.keysMu.Unlock()
	if keys != nil {
		p := keys.Protection()
		return &p, nil
	}
	if failure == nil {
		return nil, nil
	}
	p, problem := api.SealFailed, failure.Error()
	if errors.Is(failure, errKeyStoreSetup) {
		p = api.SetupRequired
	}
	if len(problem) > 200 {
		problem = problem[:200]
	}
	return &p, &problem
}
