package agent

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// poolUnit is a unit whose route goes to a pool of two replicas that the proxy probes at
// /health, which each replica answers while its health file exists.
type poolUnit struct {
	backendUnit
	replicas  []string
	instances []uuid.UUID
}

func (h isolationHost) poolUnit(prefix, answer string) poolUnit {
	h.t.Helper()
	u := poolUnit{backendUnit: h.f.unit(prefix, answer)}
	name := deployment.NetworkName(u.id)
	for range 2 {
		instance := uuid.New()
		created, err := h.f.docker.ContainerCreate(h.t.Context(), client.ContainerCreateOptions{
			Name:             deployment.UnitName(instance),
			Config:           &container.Config{Image: execution.TestImage, Cmd: []string{"sh", "-c", "mkdir -p /www && echo " + answer + " > /www/index.html && touch /www/health && exec httpd -f -p 8080 -h /www"}, Labels: map[string]string{labelServer: h.server.String(), "com.astilba.unit": u.id.String()}},
			HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(name)},
			NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{name: {Aliases: []string{deployment.ServiceName(instance)}}}},
		})
		if err != nil {
			h.t.Fatal(err)
		}
		h.t.Cleanup(func() {
			if _, err := h.f.docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
				h.t.Error(err)
			}
		})
		if _, err = h.f.docker.ContainerStart(h.t.Context(), created.ID, client.ContainerStartOptions{}); err != nil {
			h.t.Fatal(err)
		}
		u.replicas = append(u.replicas, created.ID)
		u.instances = append(u.instances, instance)
	}
	return u
}

// plan publishes u's route to its pool.
func (h isolationHost) poolPlan(u poolUnit) deployment.Plan {
	p := h.plan(u.backendUnit, u.set().Routes)
	p.Recipe = deployment.ReplicaRecipe
	app := &p.Specification.Applications[0]
	app.Replicas = 2
	app.Health = &domain.HealthCheck{Path: "/health", Port: 8080}
	p.Instances = map[string]uuid.UUID{u.app.String(): u.instances[0]}
	p.ReplicaInstances = map[string][]uuid.UUID{u.app.String(): u.instances[1:]}
	return p
}

// run runs script in the replica id and waits for it.
func (h isolationHost) run(id, script string) {
	h.t.Helper()
	h.exec(id, []string{"sh", "-c", script})
}

// healthy makes every replica of u answer the proxy's probe, or fail it.
func (h isolationHost) healthy(u poolUnit, up bool) {
	cmd := []string{"rm", "-f", "/www/health"}
	if up {
		cmd = []string{"touch", "/www/health"}
	}
	for _, id := range u.replicas {
		h.exec(id, cmd)
	}
}

// exec runs cmd in the container id and waits for it.
func (h isolationHost) exec(id string, cmd []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exec, err := h.f.docker.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd})
	if err == nil {
		_, err = h.f.docker.ExecStart(ctx, exec.ID, client.ExecStartOptions{})
	}
	for err == nil {
		status, inspectErr := h.f.docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
		if err = inspectErr; err != nil || !status.Running {
			if err == nil && status.ExitCode != 0 {
				err = errors.New(strings.Join(cmd, " ") + " failed")
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		h.t.Error(err)
	}
}

// A pool's route must not answer 503 once its publish reports it ready. The proxy counts a
// new pool's servers up and first probes them 5 seconds after loading the file; a probe
// that fails then takes the server out until the next, so a pool whose servers the proxy
// could not yet reach answered 503 for about 5 seconds after the publish had succeeded.
// Here the replicas pass their own readiness but fail the proxy's probe for the first 7
// seconds after the file is published, as a path to them that is still settling would.
func TestNativeRoutedPoolServesOnceReady(t *testing.T) {
	h := newIsolationHost(t)
	u := h.poolUnit("7", "pool")
	h.removeProxy()
	h.healthy(u, false)
	restored := make(chan struct{})
	var arm sync.Once
	h.e.Point = func(point string) {
		if point == "route-published" {
			arm.Do(func() {
				// A failed publish publishes again to restore, and the test may end first.
				timer := time.AfterFunc(7*time.Second, func() { h.healthy(u, true); close(restored) })
				t.Cleanup(func() {
					if !timer.Stop() {
						<-restored
					}
				})
			})
		}
	}
	if err := h.e.publishVisualRoutes(t.Context(), h.poolPlan(u)); err != nil {
		t.Fatal(err)
	}
	h.e.Point = nil
	select {
	case <-restored:
	default:
		t.Fatal("the publish reported ready while no server passed the proxy's probe")
	}
	// The proxy probes the pool twice in this time.
	for end := time.Now().Add(12 * time.Second); time.Now().Before(end); {
		if got, err := h.f.request(proxyName, u.domain); err != nil || got != "pool" {
			t.Fatalf("the ready route answered %q: %v", got, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A pool whose servers never pass the proxy's probe must fail its publish within a bound,
// saying which route and server failed and why, and leave the previous routes serving.
func TestNativeUnreachablePoolFailsItsPublish(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("3", "steady")
	u := h.poolUnit("4", "unreachable")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatal(err)
	}
	h.healthy(u, false)
	started := time.Now()
	err := h.e.publishVisualRoutes(t.Context(), h.poolPlan(u))
	if took := time.Since(started); took > routeServerWait+15*time.Second {
		t.Fatalf("the failing publish took %s", took)
	}
	if err == nil {
		t.Fatal("a pool the proxy cannot probe was published")
	}
	for _, want := range []string{"previous routes were restored", "the proxy cannot route to every server", "route " + u.domain, "failed the proxy's health check"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure %q does not say %q", err, want)
		}
	}
	// What the server answered stays in the agent's log.
	if strings.Contains(err.Error(), "server returned error") {
		t.Fatalf("the failure %q carries the server's answer", err)
	}
	if note := routeServersNote(err, "Routing failed.", nil); !strings.Contains(note, "The proxy could not route to every server of the routes: route "+u.domain) {
		t.Fatalf("the note %q does not say why", note)
	}
	state, err := h.j.routing()
	if err != nil || state.Pending || len(state.Current.Sets) != 1 || state.Current.Sets[steady.id.String()].UnitID != steady.id {
		t.Fatalf("the failed publish did not restore the previous routes: %+v %v", state, err)
	}
	h.f.answers(proxyName, steady.domain, "steady")
	if got, err := h.f.request(proxyName, u.domain); err == nil {
		t.Fatalf("the failed route answered %q", got)
	}
	h.probesLeftNothing()
}

// probesLeftNothing fails unless every probe ended with its round: no probe container
// remains, and the proxy, which reaps no process it did not start, runs only itself.
func (h isolationHost) probesLeftNothing() {
	h.t.Helper()
	if _, err := h.f.docker.ContainerInspect(context.Background(), routeProbeName(h.server.String()), client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		h.t.Fatalf("a route probe container remains: %v", err)
	}
	processes, _, ok, err := h.e.proxyExec(context.Background(), []string{"ps", "-o", "stat,comm"}, 16<<10)
	if err != nil || !ok {
		h.t.Fatalf("listing the proxy's processes: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(processes)), "\n")[1:] {
		if fields := strings.Fields(line); len(fields) < 2 || (fields[1] != "traefik" && fields[1] != "ps") {
			h.t.Fatalf("the probes left processes in the proxy:\n%s", processes)
		}
	}
}

// A probe that follows a redirect to https starts a TLS helper that outlives wget, and a
// server that holds the handshake keeps it running. Neither may outlive the probe's round,
// whether the round ends at the probe's bound or because the publish gave up first, even
// while Docker was still creating the probe. No probe left behind holds up a round.
func TestNativeRedirectedProbeLeavesNothing(t *testing.T) {
	h := newIsolationHost(t)
	u := h.poolUnit("9", "pool")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.poolPlan(u)); err != nil {
		t.Fatal(err)
	}
	host := deployment.BackendHost(u.id, deployment.ServiceName(u.instances[0]))
	h.run(u.replicas[0], `mkdir -p /www/cgi-bin && printf '#!/bin/sh\nprintf "Status: 302 Found\\r\\nLocation: https://`+host+`:8443/\\r\\n\\r\\n"\n' > /www/cgi-bin/redirect && chmod +x /www/cgi-bin/redirect && (nc -lk -p 8443 -e sleep 30 >/dev/null 2>&1 &)`)
	probe := "http://" + host + ":8080/cgi-bin/redirect"
	failed, _, err := h.e.probeRound(t.Context(), []string{probe})
	if err != nil || failed != 0 {
		t.Fatalf("the held probe gave %d, %v", failed, err)
	}
	h.probesLeftNothing()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, _, err = h.e.probeRound(ctx, []string{probe}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the cancelled round gave %v", err)
	}
	h.probesLeftNothing()
	// A probe another server's agent left, as one enrolled on this machine before might,
	// holds up no round and stays. This server's own is removed.
	left := func(name string, labels map[string]string) string {
		t.Helper()
		created, err := h.f.docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Labels: labels}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = h.f.docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
		})
		return created.ID
	}
	server, other := h.server.String(), uuid.NewString()
	foreign := left(routeProbeName(other), map[string]string{labelServer: other, labelKind: "route-probe-v1"})
	stale := left(routeProbeName(server), map[string]string{labelServer: server, labelKind: "route-probe-v1"})
	// PLAT-618. Docker goes on creating a container after its client gives up, so a round
	// that ended while Docker created its probe left the probe behind. It never started, so
	// AutoRemove never took it.
	ended, end := context.WithCancel(t.Context())
	defer end()
	cut := &probeCreateCut{name: routeProbeName(h.server.String()), end: end}
	agent := h.e.Docker
	h.dockerThrough(func(base http.RoundTripper) http.RoundTripper {
		cut.base = base
		return cut
	})
	if _, _, err = h.e.probeRound(ended, []string{probe}); err == nil || !cut.cut.Load() {
		t.Fatalf("the round that ended while Docker created its probe gave %v", err)
	}
	h.e.Docker = agent
	h.probesLeftNothing()
	if _, err = h.f.docker.ContainerInspect(t.Context(), stale, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("this server's probe left behind remains: %v", err)
	}
	health := "http://" + host + ":8080/health"
	// A container under this server's probe's name that is not its probe stays, and stops
	// the round.
	squatter := left(routeProbeName(server), map[string]string{labelServer: server})
	if _, _, err = h.e.probeRound(t.Context(), []string{health}); err == nil || !strings.Contains(err.Error(), "ownership conflict") {
		t.Fatalf("a round under a name another container holds gave %v", err)
	}
	for _, id := range []string{foreign, squatter} {
		if _, err = h.f.docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); err != nil {
			t.Fatalf("a container that is not this server's probe was removed: %v", err)
		}
	}
}

// probeCreateCut is a transport to Docker that, once Docker has created the probe name,
// ends the round's context with end before the answer reaches the agent, as a deadline
// that passes while Docker creates a probe does. It cuts one create.
type probeCreateCut struct {
	base http.RoundTripper
	name string
	end  context.CancelFunc
	cut  atomic.Bool
}

func (c *probeCreateCut) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/containers/create") || r.URL.Query().Get("name") != c.name || c.cut.Swap(true) {
		return c.base.RoundTrip(r)
	}
	response, err := c.base.RoundTrip(r.WithContext(context.WithoutCancel(r.Context())))
	c.end()
	// A transport gives up on a request whose context has ended, whatever Docker did.
	if err == nil && r.Context().Err() != nil {
		response.Body.Close()
		return nil, r.Context().Err()
	}
	return response, err
}

// A round may probe as many servers as an environment can run, and quick probes must not
// add up to the probe container's process limit, whether it stops at a failure or not.
func TestNativeFullProbeRoundPasses(t *testing.T) {
	h := newIsolationHost(t)
	u := h.poolUnit("a", "pool")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.poolPlan(u)); err != nil {
		t.Fatal(err)
	}
	host := deployment.BackendHost(u.id, deployment.ServiceName(u.instances[0]))
	probes := []string{}
	for n := range domain.MaxRuntimeInstances {
		probes = append(probes, "http://"+host+":8080/health?n="+strconv.Itoa(n))
	}
	if failed, stderr, err := h.e.probeRound(t.Context(), probes); err != nil || failed != -1 {
		t.Fatalf("a round of healthy probes gave %d, %v: %s", failed, err, stderr)
	}
	h.probesLeftNothing()
	// A round that sends every probe names each that fails, not only the first.
	probes[3], probes[7] = "http://"+host+":8080/missing", "http://"+host+":8081/"
	if failed, err := h.e.probeEach(t.Context(), probes); err != nil || !slices.Equal(failed, []int{3, 7}) {
		t.Fatalf("a round with two failing probes gave %v, %v", failed, err)
	}
	h.probesLeftNothing()
}
