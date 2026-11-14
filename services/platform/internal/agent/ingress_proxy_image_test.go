package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// olderProxyImage is the proxy image a preceding agent pinned.
const olderProxyImage = "traefik:v3.7.16@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// ingressDaemon is Docker running an owned proxy for s, from image, joined to the networks
// of s's units. It has the pinned proxy image only when pinned, and fails every pull with
// refusal. The proxy reports s's routes as enabled, and readiness probes succeed. It records
// every request that would change the proxy.
type ingressDaemon struct {
	server  uuid.UUID
	s       routeSnapshot
	image   string
	pinned  bool
	refusal string

	mu      sync.Mutex
	changes []string
	pulls   int
}

func (d *ingressDaemon) serve(t *testing.T) *client.Client {
	t.Helper()
	bindings := map[string]any{}
	for _, port := range d.s.Ports {
		bindings[strconv.Itoa(port)+"/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": strconv.Itoa(port)}}
	}
	gateway := 0
	if d.s.ManagedEgress {
		gateway = 100
	}
	networks := map[string]any{"astilba-ingress-network": map[string]any{"GwPriority": gateway}}
	units := map[string]any{}
	for key, set := range d.s.Sets {
		name := deployment.NetworkName(set.UnitID)
		networks[name] = map[string]any{}
		units[name] = map[string]any{"Name": name, "Id": name, "Driver": "bridge", "Labels": map[string]string{labelServer: d.server.String(), labelKind: "visual-v1", "com.astilba.unit": key, labelEnvironment: set.EnvironmentID.String()}, "Containers": map[string]any{"running-proxy": map[string]any{}}}
	}
	proxy := map[string]any{
		"Id":              "running-proxy",
		"State":           map[string]any{"Running": true, "Status": "running"},
		"Config":          map[string]any{"Image": d.image, "Cmd": proxyArgs(d.s), "Labels": map[string]string{labelServer: d.server.String(), labelKind: "ingress-v1"}},
		"HostConfig":      map[string]any{"PortBindings": bindings},
		"NetworkSettings": map[string]any{"Networks": networks},
	}
	bridge := map[string]any{"Name": "astilba-ingress-network", "Id": "ingress-network", "Driver": "bridge", "Labels": map[string]string{labelServer: d.server.String(), labelKind: "ingress-v1"}, "Containers": map[string]any{"running-proxy": map[string]any{}}}
	enabled := map[string]string{"status": "enabled"}
	runtime := map[string]map[string]any{"routers": {"activation-" + d.s.Revision + "@file": enabled}, "services": {}}
	for _, set := range d.s.Sets {
		for _, r := range set.Routes {
			name := deployment.UnitName(set.UnitID) + "-" + r.ID.String() + "@file"
			runtime["routers"][name], runtime["services"][name] = enabled, enabled
		}
	}
	rawdata, err := stablejson.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
		switch {
		case r.Method == "GET" && path == "/containers/"+proxyName+"/json":
			_ = stablejson.Encode(w, proxy)
		case r.Method == "GET" && path == "/networks/astilba-ingress-network":
			_ = stablejson.Encode(w, bridge)
		case r.Method == "GET" && strings.HasPrefix(path, "/networks/") && units[strings.TrimPrefix(path, "/networks/")] != nil:
			_ = stablejson.Encode(w, units[strings.TrimPrefix(path, "/networks/")])
		case r.Method == "GET" && path == "/images/"+deployment.ProxyImage+"/json" && d.pinned:
			fmt.Fprint(w, `{"Id":"sha256:pinned"}`)
		case r.Method == "GET" && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"No such image"}`)
		case r.Method == "POST" && path == "/images/create":
			d.pulls++
			w.WriteHeader(500)
			_ = stablejson.Encode(w, map[string]string{"message": d.refusal})
		// The proxy's runtime API, which route checks read through an exec.
		case r.Method == "POST" && path == "/containers/"+proxyName+"/exec":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"Id":"routes"}`)
		case r.Method == "POST" && path == "/exec/routes/start":
			_, _ = io.Copy(io.Discard, r.Body)
			conn, stream, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = stream.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			// One frame on standard output, as Docker multiplexes an exec without a TTY.
			header := []byte{byte(stdcopy.Stdout), 0, 0, 0, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(header[4:], uint32(len(rawdata)))
			_, _ = stream.Write(append(header, rawdata...))
			_ = stream.Flush()
			// Ending only the output, and closing once the client has, never resets a
			// connection the client is still reading.
			_ = conn.(*net.TCPConn).CloseWrite()
			go func() {
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
		case r.Method == "GET" && path == "/exec/routes/json":
			fmt.Fprint(w, `{"Running":false,"ExitCode":0}`)
		// Readiness probes, which answer at once.
		case r.Method == "POST" && path == "/containers/create" && strings.HasPrefix(r.URL.Query().Get("name"), "astilba-health-"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"Id":"probe"}`)
		case r.Method == "POST" && path == "/containers/probe/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && path == "/containers/probe/wait":
			fmt.Fprint(w, `{"StatusCode":0}`)
		case r.Method == "DELETE" && path == "/containers/probe":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE" && path == "/containers/running-proxy":
			d.changes = append(d.changes, r.Method+" "+path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET":
			// Candidates, release commands and observations the test doesn't run.
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"absent"}`)
		default:
			d.changes = append(d.changes, r.Method+" "+path)
			w.WriteHeader(500)
			fmt.Fprint(w, `{"message":"unexpected request"}`)
		}
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost("tcp://"+daemon.Listener.Addr().String()), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
}

// portlessRoutes keep a proxy running on the egress gateway without routes, as a
// coordinated primary's do.
func portlessRoutes() routeSnapshot {
	return routeSnapshot{ManagedEgress: true, PrivateIngress: true, Sets: map[string]deployment.RouteSet{}, Ports: []int{}, Revision: uuid.NewString(), QualifiedBackends: true}
}

// An updated agent that can't pull the proxy image it pins must leave the running proxy,
// and the routes it serves, as they were, and say what it couldn't pull. Removing the proxy
// first left a host without ingress for as long as the registry refused.
func TestUnpullableProxyImageKeepsTheRunningProxy(t *testing.T) {
	const refusal = "toomanyrequests: You have reached your pull rate limit"
	for _, c := range []struct {
		name string
		// recorded is whether the journal recorded the running proxy with its routes.
		recorded bool
		want     string
	}{
		{"the recorded proxy serves the previous routes again", true, "the ingress proxy image is unavailable; previous routes were restored"},
		{"another proxy needs attention", false, "previous-route recovery require attention"},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, plan := testVisualJournal(t)
			previous := portlessRoutes()
			d := &ingressDaemon{server: plan.ServerID, s: previous, image: olderProxyImage, refusal: refusal}
			e := &Engine{Journal: j, Docker: d.serve(t)}
			if err := e.rememberProxy(t.Context(), &previous); err != nil {
				t.Fatal(err)
			}
			if !c.recorded {
				previous.ProxyHash = "another proxy"
			}
			state := routeState{Current: previous}
			if err := j.saveRouting(state); err != nil {
				t.Fatal(err)
			}
			published := 0
			e.Point = func(point string) {
				if point == "route-published" {
					published++
				}
			}
			next, err := cloneRoutes(previous)
			if err != nil {
				t.Fatal(err)
			}
			next.Revision = uuid.NewString()

			publishErr := e.attemptRoutes(t.Context(), state, next, false, "", false)
			if unavailable := new(*proxyImageUnavailable); !errors.As(publishErr, unavailable) || (*unavailable).image != deployment.ProxyImage {
				t.Fatalf("the publish did not name the proxy image it couldn't pull: %v", publishErr)
			}
			if !strings.Contains(publishErr.Error(), c.want) || !strings.Contains(publishErr.Error(), deployment.ProxyImage) || !strings.Contains(publishErr.Error(), refusal) {
				t.Fatalf("the publish did not say what happened: %v", publishErr)
			}
			if len(d.changes) > 0 {
				t.Fatalf("the agent changed the running proxy: %q", d.changes)
			}
			if d.pulls == 0 {
				t.Fatal("the agent never tried to pull the proxy image")
			}
			after, err := j.routing()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(e.routePath())
			expected, _ := renderSnapshot(previous)
			if c.recorded {
				// Only putting the previous routes back writes the route file.
				if after.Pending || after.Current.Revision != previous.Revision || published != 1 || !bytes.Equal(raw, expected) {
					t.Fatalf("recovery did not settle on the previous routes: pending %v, %d publishes", after.Pending, published)
				}
				if note := proxyImageNote(publishErr, "", ""); !strings.HasPrefix(note, "This host could not pull the ingress proxy image "+deployment.ProxyImage+": ") {
					t.Fatalf("the note does not name the pull: %q", note)
				}
			} else if !after.Pending || published != 0 || raw != nil {
				t.Fatalf("recovery from an unrecorded proxy settled or changed the routes: pending %v, %d publishes", after.Pending, published)
			}
		})
	}
}

// A release whose candidate couldn't be routed because the host can't pull the proxy image
// still serves its previous routes. Its abort must fail the release as the candidate's
// publish did, without republishing those routes, which would only fail the same way and
// leave the release needing attention.
func TestReleaseOnAnUnpullableProxyImageFails(t *testing.T) {
	const refusal = "toomanyrequests: You have reached your pull rate limit"
	j, old := testVisualJournal(t)
	app, route := uuid.New(), uuid.New()
	image := "registry.example/app:v1"
	old.Specification.Applications = []domain.Application{{ID: app, Name: "App", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	old.Specification.Routes = []domain.Route{{ID: route, TargetID: app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	old.Instances = map[string]uuid.UUID{app.String(): uuid.New()}
	old = old.WithHash()
	candidate := old
	candidate.ID, candidate.Release = uuid.New(), &deployment.Release{ApplicationID: app, PreviousPlanID: old.ID, Options: deployment.DefaultReleaseOptions()}
	candidate.Instances = map[string]uuid.UUID{app.String(): uuid.New()}
	candidate = candidate.WithHash()

	// The host serves the old revision's routes, which a reviewed host change opened.
	opened := old
	opened.HostChanges = true
	current, err := (&Engine{}).candidateRoutes(routeSnapshot{}, opened)
	if err != nil {
		t.Fatal(err)
	}
	d := &ingressDaemon{server: old.ServerID, s: current, image: olderProxyImage, refusal: refusal}
	e := &Engine{Journal: j, Docker: d.serve(t)}
	if err = e.rememberProxy(t.Context(), &current); err != nil {
		t.Fatal(err)
	}
	raw, err := renderSnapshot(current)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		t.Fatal(err)
	}
	if err = j.saveRouting(routeState{Current: current}); err != nil {
		t.Fatal(err)
	}
	c := visualCommand(candidate, "deploy", 1)
	if _, err = j.Accept(c); err != nil {
		t.Fatal(err)
	}
	// The candidate is ready and the release is about to switch traffic to it.
	if err = j.saveRelease(releaseState{OperationID: c.Id, Plan: candidate, Old: unitState{Plan: old}, Phase: "switching"}); err != nil {
		t.Fatal(err)
	}

	if err = e.executeRelease(t.Context(), c, deployment.Payload{Action: "deploy", Plan: candidate}, Record{}); err != nil {
		t.Fatal(err)
	}
	record, err := j.Record(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	want := "This host could not pull the ingress proxy image " + deployment.ProxyImage + ": "
	if record.Report.State != "failed" || !strings.HasPrefix(record.Report.Note, want) || !strings.Contains(record.Report.Note, refusal) || !strings.Contains(record.Report.Note, "The candidate was not routed; previous routing was kept.") {
		t.Fatalf("the release ended %s: %q", record.Report.State, record.Report.Note)
	}
	if len(d.changes) > 0 {
		t.Fatalf("the agent changed the running proxy: %q", d.changes)
	}
	if d.pulls != 2 {
		t.Fatalf("the agent pulled the proxy image %d times, want once for the candidate and once to restore its previous routes", d.pulls)
	}
	state, err := j.routing()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending || state.Current.Revision != current.Revision || state.Current.ProxyHash != current.ProxyHash {
		t.Fatal("the release changed the routes the host records")
	}
	if s, err := j.release(old.UnitID); err != nil || s.Phase != "aborted" {
		t.Fatalf("the release is %q: %v", s.Phase, err)
	}
}

// A publish that adds a port another service holds must fail before it removes the running
// proxy. Only the ports that proxy holds itself have to wait for its removal to be checked.
func TestProxyPortsAreCheckedBeforeTheProxyGoes(t *testing.T) {
	hold := func(t *testing.T) int {
		t.Helper()
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		return listener.Addr().(*net.TCPAddr).Port
	}
	free := func(t *testing.T) int {
		t.Helper()
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	for _, c := range []struct {
		name string
		// held is whether the running proxy holds the port in use.
		held bool
	}{
		{"a new port in use keeps the proxy", false},
		{"a port the proxy holds waits for its removal", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, plan := testVisualJournal(t)
			busy := hold(t)
			previous := portlessRoutes()
			next := portlessRoutes()
			next.Ports = []int{busy}
			if c.held {
				// The test holds the port as the running proxy would.
				previous.Ports = []int{busy}
				port := free(t)
				for port == busy {
					port = free(t)
				}
				next.Ports = []int{min(busy, port), max(busy, port)}
			}
			d := &ingressDaemon{server: plan.ServerID, s: previous, image: deployment.ProxyImage, pinned: true}
			e := &Engine{Journal: j, Docker: d.serve(t)}
			_, err := e.applyRoutes(t.Context(), next, proxyAsNeeded, "")
			if err == nil || err.Error() != fmt.Sprintf("ingress port %d is already in use", busy) {
				t.Fatalf("the publish did not refuse the port in use: %v", err)
			}
			removed := slices.Equal(d.changes, []string{"DELETE /containers/running-proxy"})
			if c.held && !removed {
				t.Fatalf("a port the proxy holds was refused before its removal: %q", d.changes)
			}
			if !c.held && len(d.changes) > 0 {
				t.Fatalf("the agent changed the running proxy: %q", d.changes)
			}
		})
	}
}
