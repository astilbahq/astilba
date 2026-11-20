package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
)

// rotate replaces a legacy bearer at management's request. It runs under the token source's lock,
// so a bind can't send the bearer it replaces, and it never runs once the agent has a key.
func (c *Client) rotate(ctx context.Context) error {
	c.tokens.mu.Lock()
	defer c.tokens.mu.Unlock()
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	if cfg.Key != nil || cfg.BindKey != nil {
		return nil
	}
	if cfg.Pending == "" {
		pending, e := randtoken.New()
		if e != nil {
			return e
		}
		if e = c.Journal.UpdateConfig(func(next *Config) error {
			if next.Pending == "" {
				next.Pending = pending
			}
			return nil
		}); e != nil {
			return e
		}
		if cfg, e = c.Journal.Config(); e != nil {
			return e
		}
	}
	// A lost exchange response is recovered by trying the persisted candidate first. Run rotates
	// only once negotiated, so this heartbeat is never refused for want of it.
	var status api.AgentStatus
	if e = c.call(ctx, "POST", "/agent/v1/heartbeat", c.Observe(ctx), &status, cfg.Pending); e != nil {
		// That heartbeat's response said whether this controller takes the machine.
		var rotation any = api.AgentRotation{Credential: cfg.Pending, MachineId: cfg.MachineID}
		if !c.machineRotation.Load() {
			rotation = legacyRotation{Credential: cfg.Pending}
		}
		if e = c.call(ctx, "POST", "/agent/v1/rotate", rotation, nil, cfg.Credential); e != nil {
			return e
		}
	}
	return c.Journal.UpdateConfig(func(next *Config) error {
		if next.Pending != cfg.Pending {
			return errors.New("agent credential changed during rotation")
		}
		next.Credential, next.Pending = next.Pending, ""
		return nil
	})
}

// legacyRotation is the request earlier controllers accept: they reject a machine they don't read.
type legacyRotation struct {
	Credential string `json:"credential"`
}
