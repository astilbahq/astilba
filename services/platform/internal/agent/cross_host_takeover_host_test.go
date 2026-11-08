package agent

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// takeoverGroup is one coordinated deployment of a stateless web application across both hosts.
type takeoverGroup struct {
	parent    uuid.UUID
	hash      string
	units     []deployment.DistributedUnit
	manifests []hostnetwork.Manifest
}

// deploy runs every phase of the group on every host, primary first where the order matters.
func (g takeoverGroup) deploy(t *testing.T, hosts []*nativePlacementHost, primary int, between func(phase string)) {
	t.Helper()
	for index, host := range hosts {
		host.crossHost(t, g.parent, g.hash, "reserve", g.units[index], nil)
		if between != nil && index == primary {
			between("reserve")
		}
		host.command(t, host.networkCommand(&g.parent, g.manifests[index], false, nil))
	}
	for _, index := range []int{primary, 1 - primary} {
		hosts[index].crossHost(t, g.parent, g.hash, "prepare", g.units[index], nil)
	}
	hosts[primary].crossHost(t, g.parent, g.hash, "probe", g.units[primary], nil)
	hosts[primary].crossHost(t, g.parent, g.hash, "publish", g.units[primary], nil)
	if between != nil {
		between("publish")
	}
	for index, host := range hosts {
		host.crossHost(t, g.parent, g.hash, "retire", g.units[index], nil)
		host.crossHost(t, g.parent, g.hash, "complete", g.units[index], nil)
	}
}

// A host that has only run a secondary has no ingress proxy. When a reviewed plan makes its unit
// the primary, its agent prepares the dedicated ingress gateway during reserve, the private links
// that need it install, and the host then serves the environment from both hosts.
//
// In a replacement the revoked primary leaves the group and another secondary stays, which takes
// three hosts. With the fixture's two, the former primary's host stands in for that remaining
// secondary with a new unit, and its own first-group pool is left as a revoked host's would be.
func TestNativeCrossHostTakeover(t *testing.T) {
	ctx, hosts := nativePlacementHosts(t)
	loadNativeImages(ctx, t, hosts, execution.TestImage, deployment.ProxyImage)
	former, next := hosts[0], hosts[1]
	app, image := uuid.New(), execution.TestImage
	script := `mkdir -p /tmp/www/cgi-bin
printf '#!/bin/sh\nprintf "Content-Type: text/plain\\r\\n\\r\\n"\nhostname\n' > /tmp/www/cgi-bin/health
chmod +x /tmp/www/cgi-bin/health
exec httpd -f -p 8080 -h /tmp/www`
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sh", "-c", script}, Health: &domain.HealthCheck{Port: 8080, Path: "/cgi-bin/health"}}},
		Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "takeover.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	initial := deployment.Plan{ID: uuid.New(), ServerID: former.server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	r := former.visual(t, "resolve", initial, nil)
	var observed deployment.Observation
	if err := json.Unmarshal(*r.Observation, &observed); err != nil {
		t.Fatal(err)
	}
	initial.Images, initial.ExpectedState = observed.Images, observed.StateHash
	initial = initial.WithHash()
	former.visual(t, "deploy", initial, nil)

	network := uuid.New()
	base := []hostnetwork.Manifest{}
	for index, host := range hosts {
		membership := uuid.New()
		identity := host.call(t, hostRequest{Membership: &membership}).Identity
		if identity == nil {
			t.Fatal("missing host-local network identity")
		}
		base = append(base, hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: membership, ServerID: host.server, Generation: 1, Prefix: "10.220.0.0/24", Address: fmt.Sprintf("10.220.0.%d", index+1), PublicKey: identity.PublicKey, ListenPort: 51820, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}})
	}
	for index := range hosts {
		other := 1 - index
		base[index].Peers = []hostnetwork.Peer{{ServerID: hosts[other].server, MembershipID: base[other].MembershipID, Address: base[other].Address, PublicKey: base[other].PublicKey, Endpoint: fmt.Sprintf("10.218.0.%d:51820", other+1)}}
		hosts[index].network(t, nil, base[index], false)
	}

	// group builds a deployment whose primary is hosts[primary], running one instance on each host.
	group := func(generation int64, primary int, unitIDs []uuid.UUID, previous []deployment.Plan, applied []*uuid.UUID, prepareIngress bool) takeoverGroup {
		t.Helper()
		g := takeoverGroup{parent: uuid.New(), manifests: make([]hostnetwork.Manifest, len(hosts))}
		groupID, endpoint, remote := uuid.New(), uuid.New(), 1-primary
		instances := []uuid.UUID{uuid.New(), uuid.New()}
		for index := range hosts {
			g.manifests[index] = base[index]
			g.manifests[index].Generation = generation
			g.manifests[index].Exports, g.manifests[index].Egress = []hostnetwork.Endpoint{}, []hostnetwork.Egress{}
		}
		port := int(20000 + generation)
		g.manifests[remote].Exports = []hostnetwork.Endpoint{{ID: endpoint, UnitID: unitIDs[remote], ResourceID: app, InstanceID: instances[remote], Port: port, ContainerPort: 8080, Sources: []hostnetwork.Source{{ServerID: hosts[primary].server, UnitID: unitIDs[primary]}}}}
		g.manifests[primary].Egress = []hostnetwork.Egress{{Ingress: true, UnitID: unitIDs[primary], ServerID: hosts[remote].server, EndpointID: endpoint, Port: port}}
		placed := spec
		placed.Applications = []domain.Application{spec.Applications[0]}
		placed.Applications[0].Replicas = 2
		placed.Placement = &domain.Placement{NetworkID: network, IngressServerID: hosts[primary].server, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 200, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: hosts[primary].server, Count: 1}, {ServerID: hosts[remote].server, Count: 1}}}}}
		backends := []deployment.PrivateEndpoint{
			{ServerID: hosts[primary].server, UnitID: unitIDs[primary], ResourceID: app, InstanceID: instances[primary], Address: deployment.ServiceName(instances[primary]), Port: 8080, ContainerPort: 8080},
			{ServerID: hosts[remote].server, UnitID: unitIDs[remote], ResourceID: app, InstanceID: instances[remote], Address: base[remote].Address, Port: port, ContainerPort: 8080},
		}
		revision := uuid.NewSHA1(groupID, []byte("revision"))
		for index, host := range hosts {
			local := &deployment.LocalPlacement{GroupID: groupID, Primary: index == primary, PrepareIngress: prepareIngress && index == primary, Network: g.manifests[index], Instances: map[string][]uuid.UUID{app.String(): {instances[index]}}, PriorInstances: map[string][]uuid.UUID{}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 200, MemoryMiB: 128}}, Outputs: map[string]deployment.PrivateEndpoint{}, Backends: map[string][]deployment.PrivateEndpoint{}}
			if ids := previous[index].InstanceIDs(app); len(ids) > 0 {
				local.PriorInstances[app.String()] = ids
			}
			if index == primary {
				local.Backends[app.String()] = backends
			}
			p := deployment.Plan{ID: uuid.New(), ServerID: host.server, EnvironmentID: initial.EnvironmentID, UnitID: unitIDs[index], RevisionID: revision, Recipe: deployment.CrossHostRecipe, Specification: placed, Local: local, Images: map[string]string{}, ExpectedAppliedID: applied[index], HostChanges: true, ExpiresAt: time.Now().Add(time.Hour), ReleaseOptions: &deployment.ReleaseOptions{DrainSeconds: 1, CommandTimeoutSeconds: 60}}
			inventory := host.call(t, hostRequest{Inventory: true}).Inventory
			if inventory == nil {
				t.Fatal("missing capacity inventory")
			}
			u := deployment.DistributedUnit{Plan: p.WithHash(), Previous: previous[index], CPUMillis: 200, MemoryMiB: 128, InventoryHash: inventory.UnboundedInventoryHash}
			r := host.crossHost(t, g.parent, strings.Repeat("0", 64), "resolve", u, nil)
			var receipt deployment.CrossHostReceipt
			if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
				t.Fatal(err)
			}
			u.Plan.Images = maps.Clone(receipt.Observation.Images)
			u.Plan.ExpectedState = receipt.Observation.StateHash
			u.Plan = u.Plan.WithHash()
			g.units = append(g.units, u)
		}
		g.hash = deployment.Hash(g.units)
		return g
	}
	answers := func(address string) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		for range 12 {
			status, body := nativePlacementRequest(t, address, "takeover.example.test")
			if status != 200 {
				t.Fatalf("%s answered %d: %s", address, status, body)
			}
			seen[strings.TrimSpace(body)] = true
		}
		return seen
	}

	secondaryUnit := uuid.New()
	first := group(2, 0, []uuid.UUID{initial.UnitID, secondaryUnit}, []deployment.Plan{initial, {}}, []*uuid.UUID{&initial.RevisionID, nil}, false)
	first.deploy(t, hosts, 0, nil)
	if seen := answers("10.219.0.1"); len(seen) != 2 {
		t.Fatalf("the first primary did not reach both hosts: %v", seen)
	}
	if _, err := next.docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("the secondary host runs an ingress proxy before taking over:", err)
	}
	ingressReady := func() bool {
		t.Helper()
		inventory := next.call(t, hostRequest{Inventory: true}).Inventory
		return inventory != nil && inventory.IngressReady != nil && *inventory.IngressReady
	}
	if ingressReady() {
		t.Fatal("the secondary host reports ingress ready, so no plan would ask it to take over")
	}

	// The secondary's own unit becomes primary; the former primary's host keeps a new secondary.
	appliedSecondary := first.units[1].Plan.RevisionID
	takeover := group(3, 1, []uuid.UUID{uuid.New(), secondaryUnit}, []deployment.Plan{{}, first.units[1].Plan}, []*uuid.UUID{nil, &appliedSecondary}, true)
	takeover.deploy(t, hosts, 1, func(phase string) {
		if !ingressReady() {
			t.Fatalf("after %s the new primary doesn't report its ingress ready", phase)
		}
		proxy, err := next.docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if phase == "reserve" && len(proxy.Container.HostConfig.PortBindings) != 0 {
			t.Fatalf("the takeover published ports before the routes were: %v", proxy.Container.HostConfig.PortBindings)
		}
	})
	if seen := answers("10.219.0.2"); len(seen) != 2 {
		t.Fatalf("the new primary did not reach both hosts: %v", seen)
	}
}
