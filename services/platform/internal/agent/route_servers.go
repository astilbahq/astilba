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

// maxProbeOutput is more than a failed probe writes, which is one line of wget's.
const maxProbeOutput = 4 << 10

// routeServersUnready is a published route that the proxy does not route to every server
// of, and why.
type routeServersUnready struct{ reason error }

func (u *routeServersUnready) Error() string {
	return "the proxy cannot route to every server of the new routes: " + u.reason.Error()
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
// the published file raw, which is what the proxy loaded.
func healthCheckedPools(raw []byte, set deployment.RouteSet) ([]routePool, error) {
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
		return nil, err
	}
	pools := []routePool{}
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
			return nil, err
		}
		if path.Host != "" || path.Scheme != "" {
			return nil, &routeServersUnready{reason: fmt.Errorf("route %s: the proxy refuses a health check path that is not relative", r.Domain)}
		}
		pool := routePool{domain: r.Domain, service: name + "@file"}
		for _, server := range balancer.Servers {
			// As the proxy builds its probe: the path against the server's URL, on the
			// check's port when it names one.
			target, err := url.Parse(server.URL)
			if err != nil {
				return nil, err
			}
			probe, err := target.Parse(balancer.HealthCheck.Path)
			if err != nil {
				return nil, err
			}
			if balancer.HealthCheck.Port != 0 {
				probe.Host = net.JoinHostPort(probe.Hostname(), strconv.Itoa(balancer.HealthCheck.Port))
			}
			pool.servers = append(pool.servers, poolServer{url: target.String(), probe: probe.String()})
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

// awaitRouteServers waits until the proxy routes every health-checked pool of unit's
// routes in s, published as raw. The proxy counts each server up from when it loads the
// file, and first probes it PoolHealthInterval later, so until then its report vouches
// for nothing: one failed probe would then take the server out for another interval, and
// a pool with no server left answers 503. So each server must pass the proxy's probe,
// sent from inside the proxy, and the proxy must still report it up once any probe it
// started before that has ended. Only unit's pools are waited for: another environment's
// server, down or not, must not stop this publish.
func (e *Engine) awaitRouteServers(ctx context.Context, s routeSnapshot, raw []byte, unit string) error {
	set, ok := s.Sets[unit]
	if !ok {
		return nil
	}
	pools, err := healthCheckedPools(raw, set)
	if err != nil || len(pools) == 0 {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, routeServerWait)
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

// probeScript sends the probe $2 once, reading no body, and kills it after $1 seconds:
// wget's own timeout only bounds each read, and an exec outlives a closed stream. The
// exec's own shell waits for wget, because the proxy reaps no process it did not start,
// as one left by busybox's timeout would be. The URL is an argument, never part of the
// script.
const probeScript = `wget -q --spider -T "$1" "$2" & probe=$!; ticks=0; while kill -0 "$probe" 2>/dev/null; do if [ "$ticks" -ge "$(($1 * 50))" ]; then kill -9 "$probe"; break; fi; sleep 0.02; ticks=$((ticks + 1)); done; wait "$probe"`

// probeRouteServers sends each server the proxy's probe from inside the proxy, so that it
// takes the proxy's path to the server, and names the first that fails. Like the proxy's,
// a probe asks once and ends at PoolHealthTimeout. Each distinct probe is sent once a
// round, since routes to one application share its servers.
//
// Why a probe failed stays in the agent's log. The answer can come from wherever the
// server or a redirect sends the probe, which is in the proxy's networks, so the error,
// which reaches the deployment's notes, names only the route and server.
func (e *Engine) probeRouteServers(ctx context.Context, pools []routePool) error {
	timeout := strconv.Itoa(int(deployment.PoolHealthTimeout / time.Second))
	passed := map[string]bool{}
	for _, pool := range pools {
		for _, server := range pool.servers {
			if passed[server.probe] {
				continue
			}
			_, stderr, ok, err := e.proxyExec(ctx, []string{"sh", "-c", probeScript, "probe", timeout, server.probe}, maxProbeOutput)
			if err != nil {
				return err
			}
			if !ok {
				slog.Warn("route server failed the proxy's health check", "route", pool.domain, "server", server.url, "reason", probeReason(stderr))
				return fmt.Errorf("route %s: server %s failed the proxy's health check", pool.domain, server.url)
			}
			passed[server.probe] = true
		}
	}
	return nil
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
