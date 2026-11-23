package agent

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// routedWithoutGateway gives the host a proxy serving u's routes without the egress gateway, as
// a host that has only published routes without host changes has.
func (h isolationHost) routedWithoutGateway(u backendUnit) routeSnapshot {
	h.t.Helper()
	previous := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, QualifiedBackends: true, Sets: map[string]deployment.RouteSet{u.id.String(): u.set()}}
	if _, err := h.e.applyRoutes(h.t.Context(), previous, proxyAsNeeded, ""); err != nil {
		h.t.Fatal(err)
	}
	if err := h.e.rememberProxy(h.t.Context(), &previous); err != nil {
		h.t.Fatal(err)
	}
	if err := h.j.saveRouting(routeState{Current: previous}); err != nil {
		h.t.Fatal(err)
	}
	return previous
}

// settled requires the journal to hold no attempt and to agree with the running proxy.
func (h isolationHost) settled() routeState {
	h.t.Helper()
	state, err := h.j.routing()
	if err != nil || state.Pending {
		h.t.Fatalf("routing journal pending %v: %v", state.Pending, err)
	}
	if drifted, err := h.e.ingressDrift(h.t.Context(), state); err != nil || drifted {
		h.t.Fatalf("the proxy drifted from the journal: %v", err)
	}
	return state
}

// A new primary's proxy moves onto the egress gateway its private links need, and the routes
// it already served keep answering.
func TestNativeTakeoverMovesTheProxyOntoTheGateway(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("a", "steady")
	h.removeProxy()
	h.routedWithoutGateway(steady)
	if err := h.e.prepareIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatal("the proxy isn't on the egress gateway")
	}
	if _, err := h.e.ingressNetworkPaths(t.Context(), h.server); err != nil {
		t.Fatal("private links can't use the proxy:", err)
	}
	if state := h.settled(); !state.Current.ManagedEgress || !state.Current.PrivateIngress {
		t.Fatalf("the journal doesn't record the gateway: %+v", state.Current)
	}
	h.f.answers(proxyName, steady.domain, "steady")
}

// A host whose proxy is already on the gateway keeps it running untouched.
func TestNativeTakeoverLeavesAReadyProxyRunning(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("b", "steady")
	h.removeProxy()
	if err := h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatal(err)
	}
	before, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorded := h.settled()
	calls := h.watchProxyCalls()
	if err = h.e.prepareIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.creates.Load() != 0 || after.Container.ID != before.Container.ID || after.Container.State.StartedAt != before.Container.State.StartedAt {
		t.Fatal("a proxy already on the gateway was restarted")
	}
	if state := h.settled(); state.Current.Revision != recorded.Current.Revision {
		t.Fatal("a proxy already on the gateway had its routes rewritten")
	}
}

// A host that routes nothing gets a proxy with no ports, for its private links alone. It keeps
// it when routes come and go.
func TestNativeTakeoverWithoutRoutesStartsAPortlessProxy(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("c", "steady")
	h.removeProxy()
	if err := h.e.prepareIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	proxy, err := h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(proxy.Container.HostConfig.PortBindings) != 0 || !ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatalf("the proxy publishes %v, on the gateway %v", proxy.Container.HostConfig.PortBindings, ingressOnGateway(t.Context(), h.f.docker))
	}
	if _, err = h.e.ingressNetworkPaths(t.Context(), h.server); err != nil {
		t.Fatal("private links can't use the proxy:", err)
	}
	h.settled()
	if err = h.e.publishVisualRoutes(t.Context(), h.plan(steady, steady.set().Routes)); err != nil {
		t.Fatal(err)
	}
	h.f.answers(proxyName, steady.domain, "steady")
	if err = h.e.publishVisualRoutes(t.Context(), h.plan(steady, nil)); err != nil {
		t.Fatal(err)
	}
	if !ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatal("removing the last route took the private links' proxy away")
	}
	h.settled()
}

// A takeover that fails puts back the routes and proxy the journal records.
func TestNativeFailedTakeoverKeepsThePreviousRoutes(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("d", "steady")
	h.removeProxy()
	previous := h.routedWithoutGateway(steady)
	calls := h.watchProxyCalls()
	armed := false
	h.e.Point = func(point string) {
		if point == "route-published" && !armed {
			armed = true
			calls.fail.Store(true)
		}
	}
	if err := h.e.prepareIngress(t.Context()); err == nil || !strings.Contains(err.Error(), "previous routes were restored") {
		t.Fatalf("the failed takeover was not recovered: %v", err)
	}
	h.e.Point = nil
	if ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatal("the failed takeover left the proxy on the gateway")
	}
	if state := h.settled(); state.Current.ManagedEgress || state.Current.PrivateIngress || state.Current.Revision != previous.Revision {
		t.Fatalf("the journal kept the failed takeover: %+v", state.Current)
	}
	h.f.answers(proxyName, steady.domain, "steady")
}

// A takeover interrupted after it recreated the proxy on the gateway, before recording that, is
// put back before it is tried again, so the journal and the proxy agree.
func TestNativeInterruptedTakeoverIsPutBack(t *testing.T) {
	h := newIsolationHost(t)
	steady := h.f.unit("e", "steady")
	h.removeProxy()
	h.routedWithoutGateway(steady)
	h.e.Point = func(point string) {
		if point == "route-verified" {
			panic("simulated process exit")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the takeover was not interrupted")
			}
		}()
		_ = h.e.prepareIngress(t.Context())
	}()
	h.e.Point = nil
	if state, err := h.j.routing(); err != nil || !state.Pending || !ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatalf("the takeover wasn't interrupted after moving the proxy: %v", err)
	}
	if err := h.e.prepareIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !ingressOnGateway(t.Context(), h.f.docker) {
		t.Fatal("the retried takeover didn't reach the gateway")
	}
	if state := h.settled(); !state.Current.ManagedEgress || !state.Current.PrivateIngress {
		t.Fatalf("the journal doesn't record the retried takeover: %+v", state.Current)
	}
	h.f.answers(proxyName, steady.domain, "steady")
}

// A host whose container network pool has no room refuses the takeover as a full host refuses a
// deployment, and keeps the routes it had.
func TestNativeTakeoverOnAFullHostChangesNothing(t *testing.T) {
	h := newIsolationHost(t)
	h.removeProxy()
	pool := netip.MustParsePrefix("10.253.254.0/25")
	if _, err := h.f.docker.NetworkCreate(t.Context(), "astilba-test-filler", client.NetworkCreateOptions{Driver: "bridge", IPAM: &network.IPAM{Config: []network.IPAMConfig{{Subnet: pool}}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := h.f.docker.NetworkRemove(context.Background(), "astilba-test-filler", client.NetworkRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	h.e.NetworkPool = pool
	err := h.e.prepareIngress(t.Context())
	if !errors.Is(err, errNetworkPoolFull) || storageState(err) != "failed" {
		t.Fatalf("the full host's takeover = %v", err)
	}
	if _, err = h.f.docker.ContainerInspect(t.Context(), proxyName, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("the refused takeover left a proxy:", err)
	}
	if _, err = h.f.docker.NetworkInspect(t.Context(), "astilba-ingress-network", client.NetworkInspectOptions{}); err == nil {
		t.Fatal("the refused takeover created the ingress network")
	}
	if state := h.settled(); state.Current.PrivateIngress {
		t.Fatal("the journal records the refused takeover")
	}
}
