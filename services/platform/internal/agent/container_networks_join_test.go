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
	"github.com/moby/moby/client"
)

// fakeNetworkDaemon serves Docker's network endpoints from an in-memory record.
type fakeNetworkDaemon struct {
	mu       sync.Mutex
	networks map[string]map[string]any
}

func newFakeNetworkDaemon(t *testing.T, subnets ...string) *client.Client {
	t.Helper()
	d := &fakeNetworkDaemon{networks: map[string]map[string]any{}}
	for i, s := range subnets {
		name := fmt.Sprintf("foreign-%d", i)
		d.networks[name] = map[string]any{"Name": name, "Id": name, "Driver": "bridge", "Labels": map[string]string{}, "IPAM": map[string]any{"Config": []map[string]string{{"Subnet": s}}}}
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

// A cross-host candidate creates its networks in reserve, before links joins the plan's
// managed network, so their subnets avoid that prefix though the host doesn't hold it yet.
func TestCandidateNetworksAvoidThePrefixItWillJoin(t *testing.T) {
	j, p := testVisualJournal(t)
	docker := newFakeNetworkDaemon(t)
	e := &Engine{Journal: j, Docker: docker}
	joining := netip.MustParsePrefix("10.252.0.0/24")
	p.Local = &deployment.LocalPlacement{Network: hostnetwork.Manifest{Prefix: joining.String()}}
	if err := e.ensureUnitNetworks(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	n, err := docker.NetworkInspect(t.Context(), deployment.NetworkName(p.UnitID), client.NetworkInspectOptions{})
	if err != nil || len(n.Network.IPAM.Config) != 1 {
		t.Fatalf("unit network: %v", err)
	}
	if subnet := n.Network.IPAM.Config[0].Subnet; !subnet.IsValid() || subnet.Overlaps(joining) || !DefaultNetworkPool.Contains(subnet.Addr()) {
		t.Fatalf("candidate network %s inside the prefix it will join", subnet)
	}
}
