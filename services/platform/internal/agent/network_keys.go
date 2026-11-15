package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
)

// errEngineBusy says host work held the engine, so nothing was tried.
var errEngineBusy = errors.New("the agent is running host work")

// sealNetworkKeys seals the private-link keys this host holds in the clear to store. It runs only
// between commands, which hold the host's network lock while they work, and it is refused while
// one runs.
func (e *Engine) sealNetworkKeys(ctx context.Context, store hostnetwork.Sealer) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()
	return hostnetwork.Host{Dir: e.Journal.dir, Keys: store}.SealKeys(ctx)
}

// keepNetworkKeysSealed seals the private-link keys this host holds in the clear once its journal
// is sealed in the format that lets them be. Earlier agents made them in the clear, and so does a
// command run before then. Once they are sealed, every key the agent makes is sealed when made,
// so one pass in each run is enough. A failure is tried again an hour later.
func (c *Client) keepNetworkKeysSealed(ctx context.Context) {
	var failed time.Time
	for {
		if store := c.Journal.networkKeyStore(); store != nil && (failed.IsZero() || time.Since(failed) >= journalRekeyInterval) {
			err := c.Engine.sealNetworkKeys(ctx, store)
			if err == nil {
				return
			}
			if !errors.Is(err, errEngineBusy) && ctx.Err() == nil {
				failed = time.Now()
				slog.Warn("the agent could not seal its private-link keys; it tries again within the hour", "error", err)
			}
		}
		if !wait(ctx, journalRekeyCheck) {
			return
		}
	}
}
