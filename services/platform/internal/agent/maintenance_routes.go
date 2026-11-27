package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/url"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
)

// maintenanceRoutes are the route servers of a unit's health-checked pools that this host's
// proxy reported up when maintenance reserved the host.
type maintenanceRoutes struct {
	Servers []routedServer
}

// routedServer is a server of a pool, named as the published file and the proxy name it.
type routedServer struct {
	Service string
	URL     string
}

// recordRouted notes, before any host stops anything, which servers of the unit's
// health-checked pools the proxy routes to, on this host and on others. Pausing takes them
// out of their pools, and the proxy takes a server back only on a probe that passes, up to
// an interval after its writer resumes, so completion waits for these again. A server the
// proxy already had out is one maintenance cannot have taken out or bring back, so it is
// not waited for: another host's writer that was already failing must not keep this host's
// reservation, and this host cannot see why it failed. A record kept from an earlier
// delivery stands, since writers may have stopped since then.
func (e *Engine) recordRouted(ctx context.Context, s *crossHostState) error {
	if s.Routed != nil {
		return nil
	}
	routed := &maintenanceRoutes{Servers: []routedServer{}}
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	raw, err := renderSnapshot(state.Current)
	if err != nil {
		return err
	}
	pools, err := unitPools(state.Current, raw, s.Unit.Plan.UnitID.String())
	// The proxy never counts up a server of a pool whose check it refuses.
	if unready := new(*routeServersUnready); errors.As(err, unready) {
		pools, err = nil, nil
	}
	if err != nil {
		return err
	}
	if len(pools) > 0 {
		runtime, err := e.readIngressRuntime(ctx)
		if err != nil {
			// A proxy that cannot say what it routes routes nothing maintenance could take away.
			slog.Warn("the proxy did not report its route servers before maintenance", "error", err)
		}
		for _, pool := range pools {
			for _, server := range pool.servers {
				if err == nil && runtime["services"][pool.service].ServerStatus[server.url] == "UP" {
					routed.Servers = append(routed.Servers, routedServer{Service: pool.service, URL: server.url})
				}
			}
		}
	}
	s.Routed = routed
	return e.Journal.saveCrossHost(*s)
}

// confirmRouted waits until the proxy routes again every server recordRouted noted that
// p's unit still publishes, except a server of an instance on this host that completion
// excuses from being ready. The routes are the settled ones, since no publish runs during
// maintenance, and a server the routes no longer name is not waited for.
func (e *Engine) confirmRouted(ctx context.Context, s crossHostState, p deployment.Plan) error {
	if s.Routed == nil || len(s.Routed.Servers) == 0 {
		return nil
	}
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	drift, err := e.ingressDrift(ctx, state)
	if err != nil {
		return err
	}
	if drift {
		return errors.New("the host's routes changed outside Astilba or were left being published, so the proxy's routing cannot be confirmed")
	}
	raw, err := renderSnapshot(state.Current)
	if err != nil {
		return err
	}
	pools, err := unitPools(state.Current, raw, p.UnitID.String())
	if err != nil {
		return err
	}
	excused := map[string]bool{}
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			if !s.mustBeReady(id) {
				excused[deployment.BackendHost(p.UnitID, deployment.ServiceName(id))] = true
			}
		}
	}
	required, err := requiredPools(pools, s.Routed.Servers, excused)
	if err != nil || len(required) == 0 {
		return err
	}
	return e.awaitPools(ctx, required, raw)
}

// requiredPools keeps of pools the servers that were recorded, but for those on an excused
// host, and the pools that keep any.
func requiredPools(pools []routePool, recorded []routedServer, excused map[string]bool) ([]routePool, error) {
	routed := map[routedServer]bool{}
	for _, server := range recorded {
		routed[server] = true
	}
	required := []routePool{}
	for _, pool := range pools {
		servers := []poolServer{}
		for _, server := range pool.servers {
			target, err := url.Parse(server.url)
			if err != nil {
				return nil, err
			}
			if routed[routedServer{Service: pool.service, URL: server.url}] && !excused[target.Hostname()] {
				servers = append(servers, server)
			}
		}
		if len(servers) > 0 {
			pool.servers = servers
			required = append(required, pool)
		}
	}
	return required, nil
}
