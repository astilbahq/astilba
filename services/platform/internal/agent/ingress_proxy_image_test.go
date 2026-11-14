package agent

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// olderProxyImage is the proxy image a preceding agent pinned.
const olderProxyImage = "traefik:v3.7.16@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// proxyImageDaemon is Docker running a proxy from olderProxyImage, without the pinned proxy
// image, failing every pull with refusal. It records every request that changes a container.
type proxyImageDaemon struct {
	mu      sync.Mutex
	changes []string
	pulls   int
}

func (d *proxyImageDaemon) serve(t *testing.T, server uuid.UUID, s routeSnapshot, refusal string) *client.Client {
	t.Helper()
	proxy := map[string]any{
		"Id":              "running-proxy",
		"State":           map[string]any{"Running": true, "Status": "running"},
		"Config":          map[string]any{"Image": olderProxyImage, "Cmd": proxyArgs(s), "Labels": map[string]string{labelServer: server.String(), labelKind: "ingress-v1"}},
		"HostConfig":      map[string]any{"PortBindings": map[string]any{}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"astilba-ingress-network": map[string]any{"GwPriority": 100}}},
	}
	bridge := map[string]any{"Name": "astilba-ingress-network", "Id": "ingress-network", "Driver": "bridge", "Labels": map[string]string{labelServer: server.String(), labelKind: "ingress-v1"}, "Containers": map[string]any{"running-proxy": map[string]any{}}}
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
		case r.Method == "GET" && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"No such image"}`))
		case r.Method == "POST" && path == "/images/create":
			d.pulls++
			w.WriteHeader(500)
			_ = stablejson.Encode(w, map[string]string{"message": refusal})
		default:
			d.changes = append(d.changes, r.Method+" "+path)
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"message":"unexpected request"}`))
		}
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
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
		{"the recorded proxy serves the previous routes again", true, "previous routes were restored"},
		{"another proxy needs attention", false, "previous-route recovery require attention"},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, plan := testVisualJournal(t)
			previous := routeSnapshot{ManagedEgress: true, PrivateIngress: true, Sets: map[string]deployment.RouteSet{}, Ports: []int{}, Revision: uuid.NewString(), QualifiedBackends: true}
			var d proxyImageDaemon
			e := &Engine{Journal: j, Docker: d.serve(t, plan.ServerID, previous, refusal)}
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
