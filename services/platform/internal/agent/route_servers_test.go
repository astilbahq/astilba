package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// The agent probes each server where the proxy does: a replica pool on the check's port,
// a cross-host pool at each endpoint's own port. Routes the proxy never probes, a single
// server or a TCP route, are not waited for, and nor is another unit's pool.
func TestHealthCheckedPoolsAreProbedWhereTheProxyProbes(t *testing.T) {
	unit, other := uuid.New(), uuid.New()
	health := &domain.HealthCheck{Path: "/ready?full=1", Port: 9000}
	replica, remote, single, tcp := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	instances := []uuid.UUID{uuid.New(), uuid.New()}
	route := func(target uuid.UUID, domainName string) domain.Route {
		return domain.Route{ID: uuid.New(), TargetID: target, Protocol: "http", Domain: domainName, Port: 8080, TLS: "off", AllowCIDRs: []string{}}
	}
	routes := []domain.Route{route(replica, "pool.example.test"), route(remote, "remote.example.test"), route(single, "single.example.test")}
	routes = append(routes, domain.Route{ID: uuid.New(), TargetID: tcp, Protocol: "tcp", Domain: "db.example.test", ListenPort: 5432, Port: 5432, TLS: "off", AllowCIDRs: []string{"10.0.0.0/8"}})
	endpoint := deployment.PrivateEndpoint{InstanceID: uuid.New(), ServerID: uuid.New(), UnitID: unit, ResourceID: remote, Address: "10.251.0.2", Port: 20000, ContainerPort: 8080}
	set := deployment.RouteSet{UnitID: unit, EnvironmentID: uuid.New(), Routes: routes, Backends: map[string]deployment.Backend{
		replica.String(): {InstanceIDs: instances, Health: health},
		remote.String():  {Endpoints: []deployment.PrivateEndpoint{endpoint}, Health: health},
		single.String():  {Address: deployment.ServiceName(single)},
		tcp.String():     {Address: deployment.ServiceName(tcp)},
	}}
	otherRoute := route(uuid.New(), "other.example.test")
	otherSet := deployment.RouteSet{UnitID: other, EnvironmentID: uuid.New(), Routes: []domain.Route{otherRoute}, Backends: map[string]deployment.Backend{otherRoute.TargetID.String(): {InstanceIDs: []uuid.UUID{uuid.New(), uuid.New()}, Health: health}}}
	s := routeSnapshot{QualifiedBackends: true, Revision: uuid.NewString(), Sets: map[string]deployment.RouteSet{unit.String(): set, other.String(): otherSet}}
	raw, err := renderSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	pools, err := healthCheckedPools(raw, set)
	if err != nil {
		t.Fatal(err)
	}
	replicaHost := func(instance uuid.UUID) string {
		return deployment.BackendHost(unit, deployment.ServiceName(instance))
	}
	want := []routePool{
		{domain: "pool.example.test", service: deployment.UnitName(unit) + "-" + routes[0].ID.String() + "@file", servers: []poolServer{
			{url: "http://" + replicaHost(instances[0]) + ":8080", probe: "http://" + replicaHost(instances[0]) + ":9000/ready?full=1"},
			{url: "http://" + replicaHost(instances[1]) + ":8080", probe: "http://" + replicaHost(instances[1]) + ":9000/ready?full=1"},
		}},
		{domain: "remote.example.test", service: deployment.UnitName(unit) + "-" + routes[1].ID.String() + "@file", servers: []poolServer{
			{url: "http://10.251.0.2:20000", probe: "http://10.251.0.2:20000/ready?full=1"},
		}},
	}
	if !reflect.DeepEqual(pools, want) {
		t.Fatalf("pools %+v, want %+v", pools, want)
	}
}

// A failed probe's reason carries the server's answer, so it is kept to one short line of
// printable text.
func TestProbeReasonKeepsOnePrintableLine(t *testing.T) {
	long := strings.Repeat("é", 300)
	for stderr, want := range map[string]string{
		"": "no reason given",
		"wget: server returned error: HTTP/1.1 500 Bad\x1b[2J\nsecond line\n": "wget: server returned error: HTTP/1.1 500 Bad[2J",
		long: strings.Repeat("é", 200),
	} {
		if got := probeReason([]byte(stderr)); got != want {
			t.Errorf("probeReason(%q) = %q, want %q", stderr, got, want)
		}
	}
}

// Only a publish whose servers the proxy could not route to says why in its note.
func TestRouteServersNoteNamesTheServer(t *testing.T) {
	unready := &routeServersUnready{reason: errors.New("route a.example.test: server http://10.0.0.1:80 failed the proxy's health check: token-value")}
	got := routeServersNote(errors.Join(errors.New("previous routes were restored"), unready), "Routing failed.", map[string]string{"TOKEN": "token-value"})
	if !strings.HasPrefix(got, "Routing failed. The proxy could not route to every new server: route a.example.test") || strings.Contains(got, "token-value") {
		t.Fatalf("note %q", got)
	}
	if got := routeServersNote(errors.New("other"), "Routing failed.", nil); got != "Routing failed." {
		t.Fatalf("note %q", got)
	}
}
