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

// betweenCommands runs step with the engine to itself. It runs only between commands, which hold
// the host's network lock while they work, and it is refused while one runs.
func (e *Engine) betweenCommands(ctx context.Context, step func(context.Context) error) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()
	return step(ctx)
}

// sealNetworkKeys seals the private-link keys this host holds in the clear to store. The keys are
// sealed first, with the engine free, so that no command, a restriction least of all, waits on the
// key store meanwhile. Sealing between commands only writes what was sealed.
func (e *Engine) sealNetworkKeys(ctx context.Context, store hostnetwork.Sealer) error {
	prepared := hostnetwork.Prepare(waitingKeyStore{e, store})
	host := hostnetwork.Host{Dir: e.Journal.dir, Keys: prepared}
	e.noteKeyStore(host.PrepareSealKeys(ctx))
	prepared.Replay()
	return e.betweenCommands(ctx, host.SealKeys)
}

// restorePrivateLinks puts back the private links this host holds that aren't in place. Their keys
// are opened first, with the engine free, so that no command waits on the key store meanwhile.
func (e *Engine) restorePrivateLinks(ctx context.Context) error {
	host := e.networkHost()
	if host.Keys != nil {
		prepared := hostnetwork.Prepare(host.Keys)
		host.Keys = prepared
		e.noteKeyStore(host.OpenMissingKeys(ctx))
		prepared.Replay()
	}
	return e.betweenCommands(ctx, host.RestoreMissing)
}

// untilDone runs step between commands until it succeeds once, once ready says it can. A step that
// finds the engine busy, or the key store not working, is tried again a minute later; one that
// fails otherwise, an hour later.
func (c *Client) untilDone(ctx context.Context, what string, ready func() bool, step func(context.Context) error) {
	var next time.Time
	for {
		if ready() && !time.Now().Before(next) {
			err := step(ctx)
			switch {
			case err == nil:
				return
			case ctx.Err() != nil, errors.Is(err, errEngineBusy):
			case errors.Is(err, hostnetwork.ErrKeyStoreUnavailable):
				slog.Warn("the agent could not "+what+" because its key store isn't working; it tries again within a minute", "error", err)
			default:
				next = time.Now().Add(journalRekeyInterval)
				slog.Warn("the agent could not "+what+"; it tries again within the hour", "error", err)
			}
		}
		if !wait(ctx, journalRekeyCheck) {
			return
		}
	}
}

// keepPrivateLinksRestored puts back the private links that aren't in place, each minute: those the
// boot restore couldn't, as when the key store wasn't working yet that early, and those a
// restriction took down without their key. One link that can't be restored doesn't hold back the
// others. A failure is logged at most once an hour.
func (c *Client) keepPrivateLinksRestored(ctx context.Context) {
	var logged time.Time
	for {
		err := c.Engine.restorePrivateLinks(ctx)
		if err != nil && !errors.Is(err, errEngineBusy) && ctx.Err() == nil && time.Since(logged) >= journalRekeyInterval {
			logged = time.Now()
			slog.Warn("the agent could not restore some private links; it tries again each minute", "error", err)
		}
		if !wait(ctx, journalRekeyCheck) {
			return
		}
	}
}

// keepNetworkKeysSealed seals the private-link keys this host holds in the clear once its journal
// is sealed in the format that lets them be. Earlier agents made keys in the clear, and so does a
// command run before then. Once they are sealed, every key the agent makes is sealed when made, so
// one pass in each run is enough.
func (c *Client) keepNetworkKeysSealed(ctx context.Context) {
	c.untilDone(ctx, "seal its private-link keys", func() bool { return c.Journal.networkKeyStore() != nil }, func(ctx context.Context) error {
		return c.Engine.sealNetworkKeys(ctx, c.Journal.networkKeyStore())
	})
}
