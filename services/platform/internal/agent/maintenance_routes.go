package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// maintenanceRoutes are the route servers of a unit's health-checked pools that this host's
// proxy reported up when maintenance reserved the host.
type maintenanceRoutes struct {
	Servers []routedServer
}

// routedServer is a server of a pool: the pool's service, as the proxy names it, and the
// backend the server reaches, as backendOf names it.
type routedServer struct {
	Service string
	Backend string
}

// backendOf names what a pool server of unit reaches, however the route file addresses it:
// a service on the unit's network, by its bare or its qualified name, or another host's
// private address, with the port. Converting a host's routes to qualified names changes
// the URL but not this.
func backendOf(unit uuid.UUID, server string) (string, error) {
	target, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	host := strings.TrimSuffix(target.Hostname(), "."+deployment.NetworkName(unit))
	return net.JoinHostPort(host, target.Port()), nil
}

// maintenancePools are the health-checked pools of unit's routes in s, published as raw,
// but for a pool whose check the proxy refuses: it never routes to that pool's servers, so
// maintenance cannot take them out.
func maintenancePools(s routeSnapshot, raw []byte, unit uuid.UUID) ([]routePool, error) {
	set, ok := s.Sets[unit.String()]
	if !ok {
		return nil, nil
	}
	pools, _, err := probedPools(raw, set)
	return pools, err
}

// recordRouted notes, before any host stops anything, which servers of the unit's
// health-checked pools the proxy routes to, on this host and on others. Pausing takes them
// out of their pools, and the proxy takes a server back only on a probe that passes, up to
// an interval after its writer resumes, so completion waits for these again. A server the
// proxy already had out is one maintenance cannot have taken out or bring back, so it is
// not waited for. A proxy that cannot say what it routes refuses the reservation, as a
// readiness probe that cannot run does, since completion could not confirm it either. A
// record kept from an earlier delivery stands, since writers may have stopped since then.
func (e *Engine) recordRouted(ctx context.Context, s *crossHostState) error {
	if s.Routed != nil {
		return nil
	}
	unit := s.Unit.Plan.UnitID
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	raw, err := renderSnapshot(state.Current)
	if err != nil {
		return err
	}
	pools, err := maintenancePools(state.Current, raw, unit)
	if err != nil {
		return err
	}
	routed := &maintenanceRoutes{Servers: []routedServer{}}
	if len(pools) > 0 {
		runtime, err := e.readIngressRuntime(ctx)
		if err != nil {
			return fmt.Errorf("the proxy did not report which route servers it routes: %w", err)
		}
		for _, pool := range pools {
			for _, server := range pool.servers {
				if runtime["services"][pool.service].ServerStatus[server.url] != "UP" {
					continue
				}
				backend, err := backendOf(unit, server.url)
				if err != nil {
					return err
				}
				routed.Servers = append(routed.Servers, routedServer{Service: pool.service, Backend: backend})
			}
		}
	}
	s.Routed = routed
	return e.Journal.saveCrossHost(*s)
}

// remoteRouteFloor is the least time completion waits for the proxy to route servers on
// other hosts, however long this host's own took: enough for the proxy to probe again a
// server it marked down, and for a round of the agent's own probes to confirm it.
const remoteRouteFloor = 2 * deployment.PoolHealthInterval

// confirmRouted waits until the proxy routes again every server recordRouted noted that
// p's unit still publishes, except a server of an instance on this host that completion
// excuses from being ready. The routes must be the settled ones the journal records.
//
// This host's servers must be routed within routeServerWait. A server on another host is
// waited for until then too, but not required: only its own host knows whether it is
// excused, or whether it ever stopped, and that host may have been revoked. So a server
// still not routed is named in the note returned, and the reservation is released anyway.
func (e *Engine) confirmRouted(ctx context.Context, s crossHostState, p deployment.Plan) (string, error) {
	if s.Routed == nil || len(s.Routed.Servers) == 0 {
		return "", nil
	}
	state, err := e.Journal.routing()
	if err != nil {
		return "", err
	}
	drift, err := e.ingressDrift(ctx, state)
	if err != nil {
		return "", err
	}
	if drift {
		return "", errors.New("the host's routes changed outside Astilba or were left being published, so the proxy's routing cannot be confirmed")
	}
	raw, err := renderSnapshot(state.Current)
	if err != nil {
		return "", err
	}
	pools, err := maintenancePools(state.Current, raw, p.UnitID)
	if err != nil {
		return "", err
	}
	excused := map[string]bool{}
	for _, app := range p.LocalApplications() {
		for _, id := range p.InstanceIDs(app.ID) {
			if !s.mustBeReady(id) {
				excused[deployment.ServiceName(id)] = true
			}
		}
	}
	local, remote, err := requiredPools(p.UnitID, pools, s.Routed.Servers, excused)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(routeServerWait)
	if len(local) > 0 {
		if err = e.awaitPools(ctx, local, raw, deadline); err != nil {
			return "", err
		}
	}
	if len(remote) == 0 {
		return "", nil
	}
	if floor := time.Now().Add(remoteRouteFloor); floor.After(deadline) {
		deadline = floor
	}
	err = e.awaitPools(ctx, remote, raw, deadline)
	unready := new(*routeServersUnready)
	if err == nil || !errors.As(err, unready) {
		return "", err
	}
	return e.unroutedNote(ctx, remote, *unready), nil
}

// requiredPools keeps of unit's pools the servers that were recorded, but for those whose
// backend host is excused, split into the pools of this host's servers and of other
// hosts'. A pool keeps only the servers it is split into, and is left out with none.
//
// A server on another host is one the route file addresses by its private address: a
// plan's endpoint on this host is always addressed by its service name.
func requiredPools(unit uuid.UUID, pools []routePool, recorded []routedServer, excused map[string]bool) (local, remote []routePool, err error) {
	routed := map[routedServer]bool{}
	for _, server := range recorded {
		routed[server] = true
	}
	local, remote = []routePool{}, []routePool{}
	for _, pool := range pools {
		here, elsewhere := []poolServer{}, []poolServer{}
		for _, server := range pool.servers {
			backend, err := backendOf(unit, server.url)
			if err != nil {
				return nil, nil, err
			}
			host, _, err := net.SplitHostPort(backend)
			if err != nil {
				return nil, nil, err
			}
			if !routed[routedServer{Service: pool.service, Backend: backend}] || excused[host] {
				continue
			}
			if _, err := netip.ParseAddr(host); err == nil {
				elsewhere = append(elsewhere, server)
			} else {
				here = append(here, server)
			}
		}
		if len(here) > 0 {
			local = append(local, routePool{domain: pool.domain, service: pool.service, servers: here})
		}
		if len(elsewhere) > 0 {
			remote = append(remote, routePool{domain: pool.domain, service: pool.service, servers: elsewhere})
		}
	}
	return local, remote, nil
}

// unroutedNote says that completion released the reservation without the proxy routing
// every server of pools, which are on other hosts, and names those the proxy does not
// report up. When it reports them all up, the agent's own probe is what failed, and the
// note says which.
func (e *Engine) unroutedNote(ctx context.Context, pools []routePool, waited *routeServersUnready) string {
	unrouted := []string{}
	if runtime, err := e.readIngressRuntime(ctx); err == nil {
		for _, pool := range pools {
			for _, server := range pool.servers {
				if status := runtime["services"][pool.service].ServerStatus[server.url]; status != "UP" {
					if status == "" {
						status = "unreported"
					}
					unrouted = append(unrouted, fmt.Sprintf("route %s: the proxy reports server %s %s", pool.domain, server.url, status))
				}
			}
		}
	}
	if len(unrouted) == 0 {
		unrouted = append(unrouted, waited.reason.Error())
	}
	return "The proxy does not yet route these servers on other hosts again, and only their own hosts can excuse them: " + strings.Join(unrouted, "; ") + "."
}
