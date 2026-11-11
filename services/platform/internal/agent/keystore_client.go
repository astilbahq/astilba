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

// keyStore returns this host's key store. While it is unavailable it is opened again at most once
// a minute, so a store that wasn't ready at start, such as a TPM early in boot, recovers without
// the agent stopping its local work. Opening runs outside the lock, so a slow store never holds up
// a heartbeat reading its state.
func (c *Client) keyStore(ctx context.Context) KeyStore {
	c.keysMu.Lock()
	keys, due := c.Keys, c.Keys == nil && c.OpenKeys != nil && (c.keysTried.IsZero() || time.Since(c.keysTried) >= keyStoreRetry)
	if due {
		c.keysTried = time.Now()
	}
	c.keysMu.Unlock()
	if !due {
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
