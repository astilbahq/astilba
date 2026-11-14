package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/client"
)

// settledRoutes is the host's routes, with any attempt that may have reached the proxy
// first put back.
func (e *Engine) settledRoutes(ctx context.Context, stableProxy bool) (routeState, error) {
	state, err := e.Journal.routing()
	if err != nil || !state.Pending {
		return state, err
	}
	if stableProxy {
		v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
		if err != nil || proxyFingerprint(v) != state.Current.ProxyHash {
			return state, errors.New("release proxy recovery requires an unchanged owned proxy")
		}
	}
	// An interrupted agent cannot tell what changed the proxy since it was recorded.
	if err = e.restoreRoutes(ctx, state.Current, false, stableProxy); err != nil {
		return state, fmt.Errorf("previous ingress recovery requires attention: %w", err)
	}
	if err = e.rememberProxy(ctx, &state.Current); err != nil {
		return state, err
	}
	state.Pending = false
	return state, e.Journal.saveRouting(state)
}

// attemptRoutes replaces the host's settled routes with next, recording the attempt first
// so an interrupted one is put back. A failed attempt restores the previous routes.
func (e *Engine) attemptRoutes(ctx context.Context, state routeState, next routeSnapshot, drift bool, unit string, stableProxy bool) error {
	state.Pending = true
	if err := e.Journal.saveRouting(state); err != nil {
		return err
	}
	policy := proxyAsNeeded
	if drift {
		policy = proxyRebuild
	}
	if created, err := e.applyRoutes(ctx, next, policy, unit); err != nil {
		reloadErr := err
		// The proxy is the one the journal records unless drift was accepted, and one this
		// attempt created is new.
		recovery := e.restoreRoutes(ctx, state.Current, !drift || created, stableProxy)
		if recovery != nil {
			return fmt.Errorf("ingress reload and previous-route recovery require attention: %w", recovery)
		}
		state.Pending = false
		if err = e.rememberProxy(ctx, &state.Current); err != nil {
			return err
		}
		if e2 := e.Journal.saveRouting(state); e2 != nil {
			return e2
		}
		if errors.As(reloadErr, new(*proxyImageUnavailable)) {
			return fmt.Errorf("the ingress proxy was not updated and previous routes were restored; deploy again once this host can pull its image: %w", reloadErr)
		}
		return fmt.Errorf("ingress reload was rejected; previous routes were restored: %w", reloadErr)
	}
	if err := e.rememberProxy(ctx, &next); err != nil {
		return err
	}
	state.Current = next
	state.Pending = false
	e.point("route-verified")
	return e.Journal.saveRouting(state)
}

// restoreRoutes puts back routes the journal records as working after an attempt to
// change them failed or was interrupted. Rewriting the file reloads the proxy in place, so
// environments the attempt did not change keep their connections, and a proxy setting the
// attempt changed recreates it only as ensureProxy decides. A proxy left running that is
// not vouched for must be the one the journal recorded: otherwise a visual publish
// recreates it, and a release or preview, whose recovery never recreates the proxy, fails
// and leaves the attempt pending. A visual publish also recreates a proxy that did not load the file. A
// route the proxy loaded but left disabled is not the proxy's fault, and recreating it
// would interrupt every environment again without changing that. When the host can't pull
// the proxy image, the proxy the journal recorded stays and serves the previous routes
// again; any other proxy stays too, and the recovery needs attention.
func (e *Engine) restoreRoutes(ctx context.Context, previous routeSnapshot, vouched, stableProxy bool) error {
	created, err := e.applyRoutes(ctx, previous, proxyRestore, "")
	if errors.Is(err, errRouteDisabled) || err != nil && stableProxy {
		return err
	}
	if err == nil {
		// A proxy just created from previous is the one a rebuild would create.
		if vouched || created || !proxyWanted(previous) {
			return nil
		}
		v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if proxyFingerprint(v) == previous.ProxyHash {
			return nil
		}
		if stableProxy {
			return errors.New("release proxy recovery requires an unchanged owned proxy")
		}
	}
	_, err = e.applyRoutes(ctx, previous, proxyRebuild, "")
	return err
}
