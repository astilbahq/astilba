package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// isolationHost is an agent publishing routes for several units on one native host.
type isolationHost struct {
	t      *testing.T
	f      backendFixture
	e      *Engine
	j      *Journal
	server uuid.UUID
}

func newIsolationHost(t *testing.T) isolationHost {
	t.Helper()
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	j, p := testVisualJournal(t)
	f := newBackendFixture(t, p.ServerID)
	return isolationHost{t: t, f: f, e: &Engine{Journal: j, Docker: f.docker}, j: j, server: p.ServerID}
}

// removeProxy is registered after the units, because their networks cannot go until the
// proxy leaves them.
func (h isolationHost) removeProxy() {
	h.t.Cleanup(func() {
		if _, err := h.f.docker.ContainerRemove(context.Background(), proxyName, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			h.t.Error(err)
		}
		if _, err := h.f.docker.NetworkRemove(context.Background(), "astilba-ingress-network", client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			h.t.Error(err)
		}
	})
}

// plan publishes routes to u's service. The first publish on a host opens its ports.
func (h isolationHost) plan(u backendUnit, routes []domain.Route) deployment.Plan {
	image := execution.TestImage
	return deployment.Plan{ID: uuid.New(), ServerID: h.server, EnvironmentID: u.environment, UnitID: u.id, Recipe: deployment.Recipe, HostChanges: true, Specification: domain.Specification{Version: 1, Applications: []domain.Application{{ID: u.app, Name: "App", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Routes: routes}}
}

// heavyRoutes are n routes to u carrying about 6 KB of response headers each.
func heavyRoutes(u backendUnit, n int) []domain.Route {
	value := strings.Repeat("x", 1000)
	routes := []domain.Route{}
	for i := range n {
		headers := map[string]string{}
		for k := range 6 {
			headers[fmt.Sprintf("X-Header-%d", k)] = value
		}
		routes = append(routes, domain.Route{ID: uuid.New(), TargetID: u.app, Protocol: "http", Domain: fmt.Sprintf("r%d.%s", i, u.domain), Port: 8080, TLS: "off", AllowCIDRs: []string{}, Advanced: &domain.RouteOptions{ResponseHeaders: headers}})
	}
	return routes
}

// Environments whose routes together pass what one routing record once held must not
// stop another environment publishing, whether it adds routes or has none. With one
// record for the host, the third environment here failed before any file changed.
func TestNativeLargeEnvironmentsLeaveRoomForOthers(t *testing.T) {
	h := newIsolationHost(t)
	units := []backendUnit{h.f.unit("1", "one"), h.f.unit("2", "two"), h.f.unit("3", "three"), h.f.unit("4", "four")}
	h.removeProxy()
	for _, u := range units[:3] {
		if err := h.e.publishVisualRoutes(t.Context(), h.plan(u, heavyRoutes(u, 15))); err != nil {
			t.Fatalf("%s could not publish its routes: %v", u.domain, err)
		}
	}
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(units[3], nil)); err != nil {
		t.Fatalf("an environment without routes could not publish: %v", err)
	}
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(units[3], []domain.Route{units[3].set().Routes[0]})); err != nil {
		t.Fatalf("an environment could not add a route: %v", err)
	}
	for _, u := range units[:3] {
		h.f.answers(proxyName, "r14."+u.domain, strings.TrimSuffix(u.domain, ".example.test"))
	}
	h.f.answers(proxyName, units[3].domain, "four")
}

// trafficWatch keeps requesting host through the proxy's published port on one
// keep-alive connection and records every request that fails, and every new connection,
// until stopped.
type trafficWatch struct {
	stop     chan struct{}
	done     sync.WaitGroup
	mu       sync.Mutex
	failures []string
	dials    int
	requests int
}

func watchTraffic(t *testing.T, host, want string) *trafficWatch {
	t.Helper()
	w := &trafficWatch{stop: make(chan struct{})}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{MaxIdleConnsPerHost: 1, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		w.mu.Lock()
		w.dials++
		w.mu.Unlock()
		return dialer.DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:80/", nil)
			request.Host = host
			response, err := httpClient.Do(request)
			failure := ""
			if err != nil {
				failure = err.Error()
			} else {
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != want {
					failure = fmt.Sprintf("%d %q %v", response.StatusCode, body, readErr)
				}
			}
			w.mu.Lock()
			w.requests++
			if failure != "" {
				w.failures = append(w.failures, failure)
			}
			w.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return w
}

func (w *trafficWatch) finish() (failures []string, dials, requests int) {
	close(w.stop)
	w.done.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failures, w.dials, w.requests
}

// One environment's failing publish must cost only that environment. Recovery restores
// the previous file in place, so another environment keeps its connection and every
// request, however often the failing environment retries; recreating the shared proxy
// to recover interrupted every environment on the host, each time.
func TestNativeFailedPublishKeepsOtherTraffic(t *testing.T) {
	h := newIsolationHost(t)
	steady, failing := h.f.unit("5", "steady"), h.f.unit("6", "failing")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatal(err)
	}
	h.f.answers(proxyName, steady.domain, "steady")
	before, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := h.j.routing()
	if err != nil {
		t.Fatal(err)
	}

	watch := watchTraffic(t, steady.domain, "steady")
	time.Sleep(500 * time.Millisecond)
	// The failing environment's network belongs to another environment as far as the
	// proxy can tell, so its publish fails after the new file is written.
	broken := h.plan(failing, failing.set().Routes)
	broken.EnvironmentID = uuid.New()
	for range 3 {
		err := h.e.publishVisualRoutes(t.Context(), broken)
		if err == nil || !strings.Contains(err.Error(), "previous routes were restored") {
			t.Fatalf("the failing publish was not recovered: %v", err)
		}
	}
	// A publish whose command ends once its file is written cannot recover, and leaves
	// its attempt for the next publish on the host, from any environment, to settle.
	interrupted, cancel := context.WithCancel(t.Context())
	h.e.Point = func(point string) {
		if point == "route-published" {
			cancel()
		}
	}
	if err = h.e.publishVisualRoutes(interrupted, h.plan(failing, failing.set().Routes)); err == nil {
		t.Fatal("the interrupted publish succeeded")
	}
	h.e.Point = nil
	if state, err := h.j.routing(); err != nil || state.Pending == nil {
		t.Fatalf("the interrupted publish left no attempt: %v", err)
	}
	if err = h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatalf("the next publish did not settle the attempt: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	failures, dials, requests := watch.finish()
	if len(failures) > 0 || dials != 1 || requests < 20 {
		t.Fatalf("another environment's traffic was interrupted: %d failures in %d requests over %d connections, first %q", len(failures), requests, dials, failures[:min(len(failures), 3)])
	}

	after, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Container.ID != before.Container.ID || after.Container.State.StartedAt != before.Container.State.StartedAt {
		t.Fatal("recovery recreated or restarted the shared proxy")
	}
	state, err := h.j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || len(state.Current.Sets) != 1 || !reflect.DeepEqual(state.Current.Sets, settled.Current.Sets) {
		t.Fatalf("recovery did not settle on the previous routes: %+v", state)
	}
	if drifted, err := h.e.ingressDrift(t.Context(), state); err != nil || drifted {
		t.Fatalf("recovery left drift: %v", err)
	}
	// The environment publishes once its own problem is fixed.
	if err = h.e.publishVisualRoutes(t.Context(), h.plan(failing, failing.set().Routes)); err != nil {
		t.Fatal(err)
	}
	h.f.answers(proxyName, failing.domain, "failing")
	h.f.answers(proxyName, steady.domain, "steady")
}

// A host whose routes a preceding agent recorded in one record, with an attempt of its
// own left unfinished, keeps serving through the upgrade. The next publish, from another
// environment, settles that attempt without restarting the proxy and moves the host to
// records per unit.
func TestNativeUpgradedHostKeepsItsRoutes(t *testing.T) {
	h := newIsolationHost(t)
	steady, interrupted, next := h.f.unit("7", "steady"), h.f.unit("8", "interrupted"), h.f.unit("9", "next")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatal(err)
	}
	current, err := h.j.routing()
	if err != nil {
		t.Fatal(err)
	}
	// The preceding agent had written its attempt's file when it stopped.
	candidate := current.Current
	candidate.Revision = uuid.NewString()
	candidate.Sets = map[string]deployment.RouteSet{steady.id.String(): steady.set(), interrupted.id.String(): interrupted.set()}
	raw, err := renderSnapshot(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicRouteFile(h.e.routePath(), raw); err != nil {
		t.Fatal(err)
	}
	if err = h.j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("routing")).Delete([]byte(routeSetPrefix + steady.id.String()))
	}); err != nil {
		t.Fatal(err)
	}
	legacyRouting(t, h.j, map[string]any{"current": current.Current, "pending": map[string]routeSnapshot{"previous": current.Current, "candidate": candidate}})
	h.f.answers(proxyName, interrupted.domain, "interrupted")
	before, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	watch := watchTraffic(t, steady.domain, "steady")
	time.Sleep(500 * time.Millisecond)
	if err = h.e.publishVisualRoutes(t.Context(), h.plan(next, next.set().Routes)); err != nil {
		t.Fatalf("the upgraded host could not publish: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	failures, dials, requests := watch.finish()
	if len(failures) > 0 || dials != 1 || requests < 20 {
		t.Fatalf("the upgrade interrupted traffic: %d failures in %d requests over %d connections, first %q", len(failures), requests, dials, failures[:min(len(failures), 3)])
	}
	after, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Container.ID != before.Container.ID || after.Container.State.StartedAt != before.Container.State.StartedAt {
		t.Fatal("settling the preceding agent's attempt restarted the shared proxy")
	}
	records := routingRecords(t, h.j)
	for _, unit := range []backendUnit{steady, next} {
		if _, ok := records[routeSetPrefix+unit.id.String()]; !ok {
			t.Fatalf("%s's routes are not recorded on their own: %v", unit.domain, records)
		}
	}
	if _, ok := records[routeSetPrefix+interrupted.id.String()]; ok {
		t.Fatal("the unfinished attempt's routes were kept")
	}
	state, err := h.j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if drifted, err := h.e.ingressDrift(t.Context(), state); err != nil || drifted {
		t.Fatalf("the upgraded host reports drift: %v", err)
	}
	h.f.answers(proxyName, next.domain, "next")
	h.f.answers(proxyName, steady.domain, "steady")
}
