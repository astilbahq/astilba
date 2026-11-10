package agent

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// fakeNetworkDaemon serves Docker's network endpoints from an in-memory record.
type fakeNetworkDaemon struct {
	mu       sync.Mutex
	networks map[string]map[string]any
}

func newFakeNetworkDaemon(t *testing.T, subnets ...string) *client.Client {
	t.Helper()
	networks := []map[string]any{}
	for i, s := range subnets {
		networks = append(networks, fakeNetwork(fmt.Sprintf("foreign-%d", i), "bridge", s))
	}
	return serveFakeNetworks(t, networks...)
}

// fakeNetwork is a local network as Docker lists it, with one subnet unless subnet is empty.
func fakeNetwork(name, driver, subnet string) map[string]any {
	config := []map[string]string{}
	if subnet != "" {
		config = append(config, map[string]string{"Subnet": subnet})
	}
	return map[string]any{"Name": name, "Id": name, "Driver": driver, "Scope": "local", "Labels": map[string]string{}, "Options": map[string]string{}, "IPAM": map[string]any{"Config": config}}
}

func serveFakeNetworks(t *testing.T, networks ...map[string]any) *client.Client {
	t.Helper()
	d := &fakeNetworkDaemon{networks: map[string]map[string]any{}}
	for _, n := range networks {
		d.networks[n["Name"].(string)] = n
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
		switch {
		case r.Method == "GET" && path == "/networks":
			list := []map[string]any{}
			for _, n := range d.networks {
				list = append(list, n)
			}
			_ = stablejson.Encode(w, list)
		case r.Method == "POST" && path == "/networks/create":
			var body map[string]any
			if err := json.UnmarshalRead(r.Body, &body); err != nil {
				w.WriteHeader(400)
				return
			}
			name, _ := body["Name"].(string)
			body["Id"] = name
			d.networks[name] = body
			fmt.Fprintf(w, `{"Id":%q}`, name)
		case r.Method == "GET" && strings.HasPrefix(path, "/networks/"):
			n, ok := d.networks[strings.TrimPrefix(path, "/networks/")]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"network not found"}`)
				return
			}
			_ = stablejson.Encode(w, n)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	docker, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return docker
}

// reserveForTest records a cross-host reservation whose plan joins prefix, as the reserve
// phase does before it creates the candidate's networks.
func reserveForTest(t *testing.T, j *Journal, p deployment.Plan, prefix netip.Prefix) {
	t.Helper()
	p.Local = &deployment.LocalPlacement{Network: hostnetwork.Manifest{Prefix: prefix.String()}}
	state := crossHostState{ParentID: uuid.New(), Unit: deployment.DistributedUnit{Plan: p}, Phase: "reserved"}
	if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "cross-host", "active", state) }); err != nil {
		t.Fatal(err)
	}
}

// A reserved host joins its plan's managed network only in the links phase, after the reserve
// phase has created the candidate's networks and, for a new primary, its ingress network. Until
// then the reservation is the host's own record of that prefix, so a network made inside it
// would make links refuse.
func TestNetworksAvoidAPrefixTheHostHasReserved(t *testing.T) {
	j, candidate := testVisualJournal(t)
	docker := newFakeNetworkDaemon(t)
	e := &Engine{Journal: j, Docker: docker}
	reserved := netip.MustParsePrefix("10.252.0.0/24")
	reserveForTest(t, j, candidate, reserved)
	candidate.Local = &deployment.LocalPlacement{Network: hostnetwork.Manifest{Prefix: reserved.String()}}
	if err := e.ensureUnitNetworks(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	n, err := docker.NetworkInspect(t.Context(), deployment.NetworkName(candidate.UnitID), client.NetworkInspectOptions{})
	if err != nil || len(n.Network.IPAM.Config) != 1 {
		t.Fatalf("unit network: %v", err)
	}
	if subnet := n.Network.IPAM.Config[0].Subnet; !subnet.IsValid() || subnet.Overlaps(reserved) || !DefaultNetworkPool.Contains(subnet.Addr()) {
		t.Fatalf("the candidate's network took %s, inside the reserved prefix", subnet)
	}
}
