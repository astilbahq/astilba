package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
)

// routeServerWait bounds how long a publish waits for the proxy to route a unit's
// health-checked pools. It covers a path to a server that takes a few seconds to settle,
// and the proxy taking back a server it marked down, which it probes again only
// PoolHealthInterval after the probe that failed.
const routeServerWait = 30 * time.Second

// maxProbeOutput is more than a round of probes writes: the number of the probe that
// failed, and a few lines of wget's about it.
const maxProbeOutput = 4 << 10

// routeServersUnready is a published route that the proxy does not route to every server
// of, and why.
type routeServersUnready struct{ reason error }

func (u *routeServersUnready) Error() string {
	return "the proxy cannot route to every server of the routes: " + u.reason.Error()
}

// routeServersNote is otherwise, followed by why the proxy could not route to a server
// when that is why a publish failed, without secrets.
func routeServersNote(err error, otherwise string, secrets map[string]string) string {
	unready := new(*routeServersUnready)
	if !errors.As(err, unready) {
		return otherwise
	}
	return otherwise + " " + safeVisualError(fmt.Errorf("The proxy could not route to every server of the routes: %w.", (*unready).reason), secrets)
}

// routePool is a health-checked pool of one route, as the published file configures it.
type routePool struct {
	domain  string
	service string
	servers []poolServer
}

// poolServer is one server of a pool: its URL, by which the proxy reports its status, and
// the URL the proxy probes it at.
type poolServer struct {
	url, probe string
}

// healthCheckedPools are the pools of set's HTTP routes that the proxy probes, read from
// the published file raw, which is what the proxy loaded. A pool whose check the proxy
// refuses is an error, since the proxy never routes to its servers.
func healthCheckedPools(raw []byte, set deployment.RouteSet) ([]routePool, error) {
	pools, refused, err := probedPools(raw, set)
	if err == nil && len(refused) > 0 {
		err = &routeServersUnready{reason: fmt.Errorf("route %s: the proxy refuses a health check path that is not relative", refused[0])}
	}
	if err != nil {
		return nil, err
	}
	return pools, nil
}

// probedPools are the pools of set's HTTP routes that the proxy probes, read from raw, and
// the domains of those whose check it refuses, which it leaves out.
func probedPools(raw []byte, set deployment.RouteSet) (pools []routePool, refused []string, err error) {
	var file struct {
		HTTP struct {
			Services map[string]struct {
				LoadBalancer struct {
					Servers []struct {
						URL string `json:"url"`
					} `json:"servers"`
					HealthCheck *struct {
						Path string `json:"path"`
						Port int    `json:"port"`
					} `json:"healthCheck"`
				} `json:"loadBalancer"`
			} `json:"services"`
		} `json:"http"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, nil, err
	}
	pools = []routePool{}
	for _, r := range set.Routes {
		if r.Protocol != "http" {
			continue
		}
		name := deployment.UnitName(set.UnitID) + "-" + r.ID.String()
		balancer := file.HTTP.Services[name].LoadBalancer
		if balancer.HealthCheck == nil {
			continue
		}
		// The proxy refuses to send a check whose path names a host or scheme, so it never
		// counts such a pool's servers up, and a probe there would leave the pool's servers.
		path, err := url.Parse(balancer.HealthCheck.Path)
		if err != nil {
			return nil, nil, err
		}
		if path.Host != "" || path.Scheme != "" {
			refused = append(refused, r.Domain)
			continue
		}
		pool := routePool{domain: r.Domain, service: name + "@file"}
		for _, server := range balancer.Servers {
			// As the proxy builds its probe: the path against the server's URL, on the
			// check's port when it names one.
			target, err := url.Parse(server.URL)
			if err != nil {
				return nil, nil, err
			}
			probe, err := target.Parse(balancer.HealthCheck.Path)
			if err != nil {
				return nil, nil, err
			}
			if balancer.HealthCheck.Port != 0 {
				probe.Host = net.JoinHostPort(probe.Hostname(), strconv.Itoa(balancer.HealthCheck.Port))
			}
			pool.servers = append(pool.servers, poolServer{url: target.String(), probe: probe.String()})
		}
		pools = append(pools, pool)
	}
	return pools, refused, nil
}

// awaitRouteServers waits until the proxy routes every health-checked pool of unit's
// routes in s, published as raw. Only unit's pools are waited for: another environment's
// server, down or not, must not stop this publish.
func (e *Engine) awaitRouteServers(ctx context.Context, s routeSnapshot, raw []byte, unit string) error {
	pools, err := unitPools(s, raw, unit)
	if err != nil || len(pools) == 0 {
		return err
	}
	return e.awaitPools(ctx, pools, raw, time.Now().Add(routeServerWait))
}

// unitPools are the health-checked pools of unit's routes in s, published as raw.
func unitPools(s routeSnapshot, raw []byte, unit string) ([]routePool, error) {
	set, ok := s.Sets[unit]
	if !ok {
		return nil, nil
	}
	return healthCheckedPools(raw, set)
}

// awaitPools waits until the proxy routes every server of pools, published as raw, or the
// deadline passes. The proxy counts each server up from when it loads the file, and first probes it
// PoolHealthInterval later, so until then its report vouches for nothing: one failed probe
// would then take the server out for another interval, and a pool with no server left
// answers 503. It also probes a server it has marked down only every PoolHealthInterval.
// So each server must pass the proxy's probe, sent over the proxy's network, and the proxy
// must still report it up once any probe it started before that has ended.
func (e *Engine) awaitPools(ctx context.Context, pools []routePool, raw []byte, deadline time.Time) error {
	wait, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	last := errors.New("no server was probed in time")
	for {
		err := e.probeRouteServers(wait, pools)
		if err == nil {
			err = sleepContext(wait, deployment.PoolHealthTimeout+500*time.Millisecond)
			if err == nil {
				err = e.routeServersUp(wait, pools)
			}
		}
		if err == nil {
			current, readErr := os.ReadFile(e.routePath())
			if readErr != nil || !bytes.Equal(current, raw) {
				return errors.New("published route file changed before its servers were confirmed")
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if wait.Err() == nil {
			last = err
		}
		select {
		case <-wait.Done():
			return &routeServersUnready{reason: last}
		case <-time.After(time.Second):
		}
	}
}

// probeRouteServers sends each server the proxy's probe from the proxy's network
// namespace, so that it takes the proxy's path to the server, and names the first that
// fails. Like the proxy's, a probe asks once and ends at PoolHealthTimeout. Each distinct
// probe is sent once a round, since routes to one application share its servers.
//
// Why a probe failed stays in the agent's log. The answer can come from wherever the
// server or a redirect sends the probe, which is in the proxy's networks, so the error,
// which reaches the deployment's notes, names only the route and server.
func (e *Engine) probeRouteServers(ctx context.Context, pools []routePool) error {
	type target struct {
		pool   routePool
		server poolServer
	}
	probes, targets, seen := []string{}, []target{}, map[string]bool{}
	for _, pool := range pools {
		for _, server := range pool.servers {
			if !seen[server.probe] {
				seen[server.probe] = true
				probes = append(probes, server.probe)
				targets = append(targets, target{pool, server})
			}
		}
	}
	failed, stderr, err := e.probeRound(ctx, probes)
	if err != nil || failed < 0 {
		return err
	}
	t := targets[failed]
	slog.Warn("route server failed the proxy's health check", "route", t.pool.domain, "server", t.server.url, "reason", probeReason(stderr))
	return fmt.Errorf("route %s: server %s failed the proxy's health check", t.pool.domain, t.server.url)
}

// routeServersUp says whether the proxy reports every server of pools up.
func (e *Engine) routeServersUp(ctx context.Context, pools []routePool) error {
	runtime, err := e.readIngressRuntime(ctx)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		for _, server := range pool.servers {
			status := runtime["services"][pool.service].ServerStatus[server.url]
			if status != "UP" {
				if status == "" {
					status = "unreported"
				}
				return fmt.Errorf("route %s: the proxy reports server %s %s", pool.domain, server.url, status)
			}
		}
	}
	return nil
}

// probeReason is the first line wget wrote about a failed probe, kept to printable text
// because an answer from the server's side is in it.
func probeReason(stderr []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(stderr)), "\n")
	line = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, line)
	if runes := []rune(line); len(runes) > 200 {
		line = string(runes[:200])
	}
	if line == "" {
		return "no reason given"
	}
	return line
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
