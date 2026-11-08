package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// backendFixture holds a Docker client and removes what the test creates on it.
type backendFixture struct {
	t      *testing.T
	docker *client.Client
	server uuid.UUID
}

// backendUnit is one environment's unit, its network and a service answering its own name.
type backendUnit struct {
	id, environment, app, route uuid.UUID
	domain                      string
	container                   string
}

func newBackendFixture(t *testing.T, server uuid.UUID) backendFixture {
	t.Helper()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	pull, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	for _, image := range []string{deployment.ProxyImage, execution.TestImage} {
		if _, err = docker.ImageInspect(pull, image); errdefs.IsNotFound(err) {
			err = (&Engine{Docker: docker}).pullImage(pull, image)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return backendFixture{t: t, docker: docker, server: server}
}

func (f backendFixture) network(name string, labels map[string]string) {
	f.t.Helper()
	if _, err := f.docker.NetworkCreate(f.t.Context(), name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels}); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		if _, err := f.docker.NetworkRemove(context.Background(), name, client.NetworkRemoveOptions{}); err != nil {
			f.t.Error(err)
		}
	})
}

// unit starts a service on a new unit whose id begins with prefix, because the order of
// network names decides which one Docker DNS asks first. It also claims each given name,
// as an alias and as the hostname, the two ways a container can declare a DNS name.
func (f backendFixture) unit(prefix, answer string, claims ...string) backendUnit {
	f.t.Helper()
	u := backendUnit{id: uuid.MustParse(prefix + uuid.NewString()[len(prefix):]), environment: uuid.New(), app: uuid.New(), route: uuid.New(), domain: answer + ".example.test"}
	labels := map[string]string{labelServer: f.server.String(), labelKind: "visual-v1", "com.astilba.unit": u.id.String(), labelEnvironment: u.environment.String()}
	name := deployment.NetworkName(u.id)
	f.network(name, labels)
	config := &container.Config{Image: execution.TestImage, Cmd: []string{"sh", "-c", "mkdir -p /www && echo " + answer + " > /www/index.html && exec httpd -f -p 8080 -h /www"}, Labels: labels}
	if len(claims) > 0 {
		config.Hostname = claims[0]
	}
	created, err := f.docker.ContainerCreate(f.t.Context(), client.ContainerCreateOptions{
		Name:             deployment.UnitName(u.app),
		Config:           config,
		HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(name)},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{name: {Aliases: append(claims, deployment.ServiceName(u.app))}}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	u.container = created.ID
	f.t.Cleanup(func() {
		if _, err := f.docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			f.t.Error(err)
		}
	})
	if _, err = f.docker.ContainerStart(f.t.Context(), created.ID, client.ContainerStartOptions{}); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (u backendUnit) set() deployment.RouteSet {
	route := domain.Route{ID: u.route, TargetID: u.app, Protocol: "http", Domain: u.domain, Port: 8080, TLS: "off", AllowCIDRs: []string{}}
	return deployment.RouteSet{UnitID: u.id, EnvironmentID: u.environment, Routes: []domain.Route{route}, Backends: map[string]deployment.Backend{u.app.String(): {Address: deployment.ServiceName(u.app)}}}
}

// request asks the proxy from inside its own container, so the test publishes no port.
func (f backendFixture) request(proxy, host string) (string, error) {
	ctx, cancel := context.WithTimeout(f.t.Context(), 20*time.Second)
	defer cancel()
	exec, err := f.docker.ExecCreate(ctx, proxy, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"wget", "-T", "5", "-qO-", "--header", "Host: " + host, "--header", "Cookie: session=victim-secret", "http://127.0.0.1:80/"}})
	if err != nil {
		f.t.Fatal(err)
	}
	attached, err := f.docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	defer attached.Close()
	var output, stderr bytes.Buffer
	if _, err = stdcopy.StdCopy(&output, &stderr, attached.Reader); err != nil {
		f.t.Fatal(err)
	}
	status, err := f.docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	if status.ExitCode != 0 {
		return "", fmt.Errorf("request failed: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

// answers waits for the first answer from host, then requires it to stay the same.
func (f backendFixture) answers(proxy, host, want string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	for {
		got, err := f.request(proxy, host)
		if err == nil {
			if got != want {
				f.t.Fatalf("%s answered with %q, want %q", host, got, want)
			}
			break
		}
		select {
		case <-ctx.Done():
			f.t.Fatalf("%s never answered: %v", host, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	for range 5 {
		if got, err := f.request(proxy, host); err != nil || got != want {
			f.t.Fatalf("%s answered with %q, want %q: %v", host, got, want, err)
		}
	}
}

// A unit that declares another unit's backend name must get none of its requests, whether
// Docker DNS asks its network before or after the owner's, and whether or not the owner is
// running. Without qualified names the first claimant answers throughout, and the last one
// while the owner is stopped.
func TestPinnedProxyReachesOnlyTheOwningUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker proxy")
	}
	if os.Getenv("PLAT_APP_DOCKER_HOST") == "" {
		t.Fatal("PLAT_APP_DOCKER_HOST must explicitly select a disposable daemon")
	}
	f := newBackendFixture(t, uuid.New())
	owner := f.unit("8", "owner")
	claimed := deployment.ServiceName(owner.app)
	first := f.unit("0", "first-claimant", claimed)
	last := f.unit("f", "last-claimant", claimed)
	ingress := "astilba-test-ingress-" + uuid.NewString()
	f.network(ingress, map[string]string{labelServer: f.server.String(), labelKind: "ingress-v1"})
	snapshot := routeSnapshot{QualifiedBackends: true, Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{}}
	for _, u := range []backendUnit{owner, first, last} {
		snapshot.Sets[u.id.String()] = u.set()
	}
	raw, err := renderSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	proxy, _ := startRouteProxy(t, f.docker, snapshot, raw, ingress, deployment.NetworkName(first.id), deployment.NetworkName(owner.id), deployment.NetworkName(last.id))

	f.answers(proxy, owner.domain, "owner")
	f.answers(proxy, first.domain, "first-claimant")
	f.answers(proxy, last.domain, "last-claimant")

	immediately := 0
	if _, err = f.docker.ContainerStop(t.Context(), owner.container, client.ContainerStopOptions{Timeout: &immediately}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if got, err := f.request(proxy, owner.domain); err == nil {
			t.Fatalf("a stopped owner's route answered with %q", got)
		}
	}
	f.answers(proxy, first.domain, "first-claimant")

	if _, err = f.docker.ContainerStart(t.Context(), owner.container, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	f.answers(proxy, owner.domain, "owner")
}

// An upgraded agent inherits a route file it must not call drift, and has to replace it
// while the routes keep answering, without any command arriving.
func TestNativeRouteBackendConversion(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	j, p := testVisualJournal(t)
	f := newBackendFixture(t, p.ServerID)
	ctx := t.Context()
	e := &Engine{Journal: j, Docker: f.docker}
	one, two := f.unit("8", "one"), f.unit("9", "two")
	// Registered after the units, because their networks cannot go until the proxy leaves them.
	t.Cleanup(func() {
		if _, err := f.docker.ContainerRemove(context.Background(), proxyName, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			t.Error(err)
		}
		if _, err := f.docker.NetworkRemove(context.Background(), "astilba-ingress-network", client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			t.Error(err)
		}
	})
	publishLegacy := func() routeState {
		t.Helper()
		legacy := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{one.id.String(): one.set(), two.id.String(): two.set()}}
		if err := e.applyRoutes(ctx, legacy, false); err != nil {
			t.Fatal(err)
		}
		if err := e.rememberProxy(ctx, &legacy); err != nil {
			t.Fatal(err)
		}
		state := routeState{Current: legacy}
		if err := j.saveRouting(state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	drift := func() bool {
		t.Helper()
		state, err := j.routing()
		if err != nil {
			t.Fatal(err)
		}
		drifted, err := e.ingressDrift(ctx, state)
		if err != nil {
			t.Fatal(err)
		}
		return drifted
	}
	routeFile := func() string {
		t.Helper()
		raw, err := os.ReadFile(e.routePath())
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	qualifiedOne, bareOne := deployment.BackendHost(one.id, deployment.ServiceName(one.app))+":8080", `"http://`+deployment.ServiceName(one.app)+`:8080"`

	legacy := publishLegacy()
	if drift() {
		t.Fatal("an older agent's route file was reported as drift")
	}
	if !strings.Contains(routeFile(), bareOne) {
		t.Fatalf("fixture did not publish bare names: %s", routeFile())
	}
	f.answers(proxyName, one.domain, "one")

	if err := e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Current.QualifiedBackends || state.Current.Revision == legacy.Current.Revision || state.Pending != nil {
		t.Fatalf("conversion was not recorded: %+v", state)
	}
	if !reflect.DeepEqual(state.Current.Sets, legacy.Current.Sets) {
		t.Fatal("conversion changed route sets, which enter the state hash")
	}
	if !strings.Contains(routeFile(), qualifiedOne) || strings.Contains(routeFile(), bareOne) {
		t.Fatalf("route file was not converted: %s", routeFile())
	}
	if drift() {
		t.Fatal("conversion left drift")
	}
	f.answers(proxyName, one.domain, "one")
	f.answers(proxyName, two.domain, "two")

	// A restart after the file was written but before the journal finds the same file.
	converted := routeFile()
	if err = j.saveRouting(legacy); err != nil {
		t.Fatal(err)
	}
	if !drift() {
		t.Fatal("fixture did not leave the journal behind the file")
	}
	if err = e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	if routeFile() != converted || drift() {
		t.Fatal("an interrupted conversion was not completed")
	}

	// A changed file is left for the owner's explicit reconciliation.
	publishLegacy()
	changed := routeFile() + "\n"
	if err = os.WriteFile(e.routePath(), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if routeFile() != changed || state.Current.QualifiedBackends || !drift() {
		t.Fatal("conversion overwrote a changed route file")
	}

	// A failed conversion restores the file and is tried again, with no drift left behind.
	legacy = publishLegacy()
	broken := false
	e.Point = func(point string) {
		if point == "route-published" && !broken {
			broken = true
			if err := os.WriteFile(e.routePath(), []byte("{}"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	err = e.QualifyRouteBackends(ctx)
	e.Point = nil
	if err == nil {
		t.Fatal("a conversion the proxy never activated was reported as done")
	}
	state, err = j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if state.Current.QualifiedBackends || !strings.Contains(routeFile(), bareOne) || drift() {
		t.Fatalf("failed conversion was not restored: %s", routeFile())
	}
	f.answers(proxyName, one.domain, "one")
	if err = e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err = j.routing(); err != nil || !state.Current.QualifiedBackends {
		t.Fatalf("the retry did not convert: %v", err)
	}

	// Conversion never restarts the proxy, even where this agent would now run it differently.
	legacy = publishLegacy()
	legacy.Current.Email = "changed@example.test"
	if err = j.saveRouting(legacy); err != nil {
		t.Fatal(err)
	}
	before, err := f.docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := f.docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Container.ID != before.Container.ID {
		t.Fatal("conversion replaced the proxy")
	}
	if state, err = j.routing(); err != nil || !state.Current.QualifiedBackends || drift() {
		t.Fatalf("conversion with a differing proxy did not finish cleanly: %v", err)
	}

	// Qualified names grow what the proxy reports, which must still fit what the agent reads.
	large := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{one.id.String(): one.set()}}
	instances := []uuid.UUID{}
	for range 10 {
		instances = append(instances, uuid.New())
	}
	padding := strings.Repeat("x", 1000)
	routes := []domain.Route{}
	for i := range 100 {
		routes = append(routes, domain.Route{ID: uuid.New(), TargetID: one.app, Protocol: "http", Domain: fmt.Sprintf("large-%d.example.test", i), Port: 8080, TLS: "off", AllowCIDRs: []string{"10.0.0.0/8"}, Advanced: &domain.RouteOptions{ResponseHeaders: map[string]string{"X-A": padding, "X-B": padding}, StripPrefix: "/large", RequestsPerSecond: 10}})
	}
	set := large.Sets[one.id.String()]
	set.Routes, set.Backends = routes, map[string]deployment.Backend{one.app.String(): {InstanceIDs: instances, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	large.Sets[one.id.String()] = set
	if err = e.applyRoutes(ctx, large, false); err != nil {
		t.Fatal(err)
	}
	if err = e.rememberProxy(ctx, &large); err != nil {
		t.Fatal(err)
	}
	if err = j.saveRouting(routeState{Current: large}); err != nil {
		t.Fatal(err)
	}
	if err = e.QualifyRouteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err = j.routing(); err != nil || !state.Current.QualifiedBackends {
		t.Fatalf("routes this large did not convert: %v", err)
	}

	// Any publish converts the whole host, including units it does not change.
	publishLegacy()
	image := execution.TestImage
	p.UnitID, p.EnvironmentID = two.id, two.environment
	p.Specification.Applications = []domain.Application{{ID: two.app, Name: "Two", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}
	p.Specification.Routes = two.set().Routes
	if err = e.publishVisualRoutes(ctx, p); err != nil {
		t.Fatal(err)
	}
	state, err = j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Current.QualifiedBackends || !strings.Contains(routeFile(), qualifiedOne) {
		t.Fatalf("a publish left another unit on bare names: %s", routeFile())
	}
	f.answers(proxyName, one.domain, "one")
}

// A pending attempt belongs to its command's recovery, and a host with nothing left to
// convert has nothing to do. Each returns before touching Docker.
func TestRouteBackendConversionWaits(t *testing.T) {
	j, _ := testVisualJournal(t)
	e := &Engine{Journal: j}
	unit := uuid.New()
	legacy := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{unit.String(): {UnitID: unit, EnvironmentID: uuid.New()}}}
	for name, state := range map[string]routeState{
		"pending":   {Current: legacy, Pending: &routeAttempt{Previous: legacy, Candidate: legacy}},
		"converted": {Current: routeSnapshot{QualifiedBackends: true, Sets: legacy.Sets}},
		"no routes": {Current: routeSnapshot{Sets: map[string]deployment.RouteSet{}, Ports: []int{}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := j.saveRouting(state); err != nil {
				t.Fatal(err)
			}
			before, err := stablejson.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.QualifyRouteBackends(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, err := j.routing()
			if err != nil {
				t.Fatal(err)
			}
			after, err := stablejson.Marshal(current)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("routing state changed: %v", errors.Join(err, errors.New(string(after))))
			}
		})
	}
}

// A frozen exec must not outlast its deadline, and while a conversion waits on one the host
// must keep reporting and stop when told to: it may be Docker that is failing.
func TestStalledRouteCheckHoldsNeitherHeartbeatsNorShutdown(t *testing.T) {
	j, _ := testVisualJournal(t)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	var stalls atomic.Int32
	inspect := fmt.Sprintf(`{"Id":"proxy","Config":{"Labels":{%q:%q,%q:"ingress-v1"}},"HostConfig":{},"NetworkSettings":{"Networks":{}},"State":{"Running":true}}`, labelServer, cfg.ServerID, labelKind)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/"+proxyName+"/json"):
			_, _ = w.Write([]byte(inspect))
		case strings.HasSuffix(r.URL.Path, "/containers/"+proxyName+"/exec"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"stalled"}`))
		case strings.HasSuffix(r.URL.Path, "/exec/stalled/start"):
			stalls.Add(1)
			conn, stream, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			t.Cleanup(func() { conn.Close() })
			_, _ = stream.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = stream.Flush()
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost("tcp://"+daemon.Listener.Addr().String()), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	e := &Engine{Journal: j, Docker: docker}
	unit, app := uuid.New(), uuid.New()
	route := domain.Route{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "stalled.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}
	legacy := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Sets: map[string]deployment.RouteSet{unit.String(): {UnitID: unit, EnvironmentID: uuid.New(), Routes: []domain.Route{route}, Backends: map[string]deployment.Backend{app.String(): {Address: deployment.ServiceName(app)}}}}}
	proxy, err := docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacy.ProxyHash = proxyFingerprint(proxy)
	raw, err := renderSnapshot(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		t.Fatal(err)
	}
	if err = j.saveRouting(routeState{Current: legacy}); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	checked := make(chan error, 1)
	go func() { checked <- e.verifyRoutes(deadline, legacy) }()
	select {
	case err := <-checked:
		if err == nil || stalls.Load() != 1 {
			t.Fatalf("the check did not reach a stalled exec, or counted one as verified: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled exec outlasted its deadline")
	}

	heartbeat := make(chan struct{}, 16)
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			select {
			case heartbeat <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(management.Close)
	cfg.Origin, cfg.Enrolled, cfg.Credential = management.URL, true, "agent-credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, e)
	c.HTTP = management.Client()
	c.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	running, stop := context.WithCancel(t.Context())
	defer stop()
	stopped := make(chan error, 1)
	go func() { stopped <- c.Run(running) }()
	select {
	case <-heartbeat:
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled conversion held back the first heartbeat")
	}
	stop()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("a stalled conversion held up shutdown")
	}
}
