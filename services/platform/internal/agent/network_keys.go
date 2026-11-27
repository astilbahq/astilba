package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// errEngineBusy says host work held the engine, so nothing was tried.
var errEngineBusy = errors.New("the agent is running host work")

// betweenCommands runs step with the engine to itself: between commands, or while a command has
// lent it as it puts images into Docker, see lend. Commands hold the host's network lock while
// they work on its networks. It is refused while a command runs otherwise.
func (e *Engine) betweenCommands(ctx context.Context, step func(context.Context) error) error {
	if e.mu.TryLock() {
		defer e.mu.Unlock()
		return step(ctx)
	}
	e.lendMu.Lock()
	defer e.lendMu.Unlock()
	if !e.lent {
		return errEngineBusy
	}
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

// restorePrivateLinks puts back the private links this host holds that aren't in place, apart from
// the networks skip names. Their keys are opened first, with the engine free, so that no command
// waits on the key store meanwhile. Each network that fails is a hostnetwork.NetworkError.
func (e *Engine) restorePrivateLinks(ctx context.Context, skip func(hostnetwork.Snapshot) bool) error {
	host := e.networkHost()
	if host.Keys != nil {
		prepared := hostnetwork.Prepare(host.Keys)
		host.Keys = prepared
		e.noteKeyStore(host.OpenMissingKeys(ctx, skip))
		prepared.Replay()
	}
	// The restore replays every failure the opening met, against the intent it then holds, so its
	// own result is the one to go by.
	return e.betweenCommands(ctx, func(ctx context.Context) error { return host.RestoreMissing(ctx, skip) })
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
				// The agent logs that its key store isn't working once an hour; see waitForKeyStore.
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
// restriction took down without their key, or left half applied. One network that fails doesn't
// hold back the others; see restoreRetries. A failure is logged at most once an hour.
func (c *Client) keepPrivateLinksRestored(ctx context.Context) {
	var logged time.Time
	retry := map[uuid.UUID]restoreWait{}
	for {
		now := time.Now()
		tried := map[uuid.UUID]string{}
		err := c.Engine.restorePrivateLinks(ctx, func(s hostnetwork.Snapshot) bool {
			id, intent := s.Manifest.NetworkID, hostnetwork.Hash(s.Manifest)
			if w, ok := retry[id]; ok && w.intent == intent && now.Before(w.until) {
				return true
			}
			tried[id] = intent
			return false
		})
		if !errors.Is(err, errEngineBusy) {
			restoreRetries(retry, tried, networkFailures(err), now)
			if err != nil && ctx.Err() == nil && time.Since(logged) >= journalRekeyInterval {
				logged = time.Now()
				slog.Warn("the agent could not restore some private links; it keeps trying", "error", err)
			}
		}
		if !wait(ctx, journalRekeyCheck) {
			return
		}
	}
}

// restoreWait is how long a network waits before it is restored again, while its saved intent is
// the one that failed. A command that saves another intent ends the wait.
type restoreWait struct {
	until  time.Time
	intent string
}

// restoreRetries records when each network tried at now, with the intent it held, may be tried
// again. A network restored is tried whenever it next isn't in place. A failure that had to open
// the key from the key store waits an hour, while the intent stays the same, since each try costs
// key store calls: a key that isn't this machine's, or a link that can't come up. A key store that
// wasn't working is tried again a minute later, and so is a failure on a link that holds its key
// already, such as a restriction left half applied, which must finish as soon as it can.
func restoreRetries(retry map[uuid.UUID]restoreWait, tried map[uuid.UUID]string, failed map[uuid.UUID]hostnetwork.NetworkError, now time.Time) {
	for id, intent := range tried {
		f, ok := failed[id]
		if ok && f.NeedsKey && !errors.Is(f.Err, hostnetwork.ErrKeyStoreUnavailable) {
			retry[id] = restoreWait{now.Add(journalRekeyInterval), intent}
		} else {
			delete(retry, id)
		}
	}
}

// networkFailures is the failure of each network in err, which may join several. The first one
// found for a network is kept.
func networkFailures(err error) map[uuid.UUID]hostnetwork.NetworkError {
	out := map[uuid.UUID]hostnetwork.NetworkError{}
	var walk func(error)
	walk = func(err error) {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range joined.Unwrap() {
				walk(e)
			}
			return
		}
		var failed hostnetwork.NetworkError
		if !errors.As(err, &failed) {
			return
		}
		if _, seen := out[failed.NetworkID]; !seen {
			out[failed.NetworkID] = failed
		}
	}
	if err != nil {
		walk(err)
	}
	return out
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
