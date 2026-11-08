package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/host"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func nativeSubnet(t *testing.T, f *nativeVisualFixture, name string) (string, netip.Prefix) {
	t.Helper()
	n, err := f.docker.NetworkInspect(context.Background(), name, client.NetworkInspectOptions{})
	if err != nil || len(n.Network.IPAM.Config) != 1 {
		t.Fatalf("network %s: %v", name, err)
	}
	return n.Network.ID, n.Network.IPAM.Config[0].Subnet
}

// A stock daemon hands out about 30 networks from its built-in pools. A host takes as many
// routed environments as its ingress allows, keeps a network it made before, leaves the
// owner's own networks to Docker, and keeps clear of a range the host routes and of the
// managed network's prefix.
func TestNativeHostRunsFiftyRoutedEnvironments(t *testing.T) {
	f := newNativeVisualFixture(t)
	ctx := context.Background()
	// An unreachable route names a range the way an owner reserves one. Docker would
	// allocate over it.
	routed := netip.MustParsePrefix("10.252.0.0/24")
	if out, err := exec.Command("ip", "route", "add", "unreachable", routed.String()).CombinedOutput(); err != nil {
		t.Fatalf("reserve a range: %s", out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "route", "del", "unreachable", routed.String()).Run() })
	nativeHostRangesUnderTheUnitSandbox(t, routed)
	// A managed network installs only /32 routes, so nothing on the host shows its prefix.
	managed := netip.MustParsePrefix("10.252.1.0/24")
	held := hostnetwork.Manifest{Version: hostnetwork.Version, NetworkID: uuid.New(), MembershipID: uuid.New(), ServerID: f.server, Generation: 1, Prefix: managed.String(), Address: "10.252.1.7", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820, Peers: []hostnetwork.Peer{}, Exports: []hostnetwork.Endpoint{}, Egress: []hostnetwork.Egress{}}
	raw, _ := stablejson.Marshal(hostnetwork.Snapshot{Manifest: held, Bridges: []hostnetwork.Bridge{}})
	if err := os.MkdirAll(filepath.Join(f.state, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, "network", held.NetworkID.String()+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}

	plans := []deployment.Plan{}
	for i := range 50 {
		p, _ := f.plan(fmt.Sprintf("env%02d", i), false)
		plans = append(plans, p)
	}
	// The first environment's network was made the way earlier agents made it, by Docker.
	_, err := f.docker.NetworkCreate(ctx, deployment.NetworkName(plans[0].UnitID), client.NetworkCreateOptions{Driver: "bridge", Labels: visualLabels(plans[0], plans[0].UnitID)})
	if err != nil {
		t.Fatal(err)
	}
	earlierID, earlierSubnet := nativeSubnet(t, f, deployment.NetworkName(plans[0].UnitID))
	if DefaultNetworkPool.Overlaps(earlierSubnet) {
		t.Fatalf("Docker placed the earlier network in the agent's pool: %s", earlierSubnet)
	}
	for i, p := range plans {
		p = f.resolve(p)
		plans[i] = p
		f.finish(f.command("deploy", p, map[string]string{}))
	}
	// The owner's own networks still come from Docker's pools.
	owner, err := f.docker.NetworkCreate(ctx, "astilba-fixture-owner-"+f.server.String(), client.NetworkCreateOptions{Driver: "bridge"})
	if err != nil {
		t.Fatalf("owner network after 50 environments: %v", err)
	}
	t.Cleanup(func() { _, _ = f.docker.NetworkRemove(context.Background(), owner.ID, client.NetworkRemoveOptions{}) })

	seen := map[netip.Prefix]bool{}
	for i, p := range plans {
		f.request(fmt.Sprintf("env%02d.example.test", i), "v1")
		id, subnet := nativeSubnet(t, f, deployment.NetworkName(p.UnitID))
		if i == 0 {
			if id != earlierID || subnet != earlierSubnet {
				t.Fatalf("the earlier network moved: %s %s", id, subnet)
			}
			continue
		}
		if subnet.Bits() != networkBits || !DefaultNetworkPool.Contains(subnet.Addr()) || subnet.Overlaps(routed) || subnet.Overlaps(managed) || seen[subnet] {
			t.Fatalf("environment %d network %s", i, subnet)
		}
		seen[subnet] = true
	}
	if _, subnet := nativeSubnet(t, f, "astilba-ingress-network"); !DefaultNetworkPool.Contains(subnet.Addr()) || seen[subnet] || subnet.Overlaps(routed) || subnet.Overlaps(managed) {
		t.Fatalf("ingress network %s", subnet)
	}
}

// A host with no range left refuses an environment before creating any of it, and the
// environments it has keep serving.
func TestNativeFullHostRefusesAnotherEnvironment(t *testing.T) {
	f := newNativeVisualFixture(t)
	ctx := context.Background()
	refused := func(p deployment.Plan) {
		t.Helper()
		c := f.command("deploy", p, map[string]string{})
		if err := f.engine.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := f.engine.Journal.Record(c.Id)
		if err != nil || r.Report.State != "failed" || !strings.Contains(r.Report.Note, "pool "+f.engine.NetworkPool.String()+" has no free range left") {
			t.Fatalf("full host: %s %q %v", r.Report.State, r.Report.Note, err)
		}
		if err = f.engine.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		if _, err = f.docker.NetworkInspect(ctx, deployment.NetworkName(p.UnitID), client.NetworkInspectOptions{}); err == nil {
			t.Fatal("a refused environment created its network")
		}
		containers, err := f.engine.ownedContainers(ctx, p)
		if err != nil || len(containers) != 0 {
			t.Fatalf("a refused environment created containers: %d %v", len(containers), err)
		}
	}
	first, _ := f.plan("first", false)
	first = f.resolve(first)
	// The host's first routed environment needs its own network and the ingress network,
	// and one range holds only one of them.
	f.engine.NetworkPool = netip.MustParsePrefix("10.253.254.0/25")
	refused(first)
	if _, err := f.docker.NetworkInspect(ctx, "astilba-ingress-network", client.NetworkInspectOptions{}); err == nil {
		t.Fatal("a refused environment created the ingress network")
	}
	f.engine.NetworkPool = netip.MustParsePrefix("10.253.254.0/24")
	first.ID = uuid.New()
	first = first.WithHash()
	f.finish(f.command("deploy", first, map[string]string{}))

	second, _ := f.plan("second", false)
	refused(f.resolve(second))
	f.request("first.example.test", "v1")
}

// The agent reads the host's ranges under the shipped unit's sandbox, which has no netlink
// socket until the owner prepares managed networking.
func nativeHostRangesUnderTheUnitSandbox(t *testing.T, want netip.Prefix) {
	t.Helper()
	sandbox := []string{"--wait", "--pipe", "--quiet", "--collect"}
	section := ""
	for _, line := range strings.Split(string(host.AgentService()), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
		}
		key, _, _ := strings.Cut(line, "=")
		if section == "[Service]" && slices.Contains([]string{"NoNewPrivileges", "ProtectHome", "ProtectSystem", "RestrictAddressFamilies"}, key) {
			sandbox = append(sandbox, "-p", line)
		}
	}
	if !slices.ContainsFunc(sandbox, func(v string) bool { return strings.HasPrefix(v, "RestrictAddressFamilies=") }) {
		t.Fatal("the agent unit no longer restricts address families")
	}
	if exec.Command("systemd-run", append(sandbox, "ip", "-4", "route", "show")...).Run() == nil {
		t.Fatal("the unit sandbox allows netlink, so this check would prove nothing")
	}
	args := append(sandbox, "-E", "ASTILBA_HOST_RANGES_HELPER=1", "-E", "ASTILBA_HOST_RANGES_WANT="+want.String(), os.Args[0], "-test.v", "-test.run=^TestHostRangesSandboxHelper$")
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestHostRangesSandboxHelper") {
		t.Fatalf("host ranges under the unit sandbox: %v\n%s", err, out)
	}
}

// Re-executed under the agent unit's sandbox by TestNativeHostRunsFiftyRoutedEnvironments.
func TestHostRangesSandboxHelper(t *testing.T) {
	if os.Getenv("ASTILBA_HOST_RANGES_HELPER") != "1" {
		t.Skip("host range sandbox helper")
	}
	// No managed network state: the helper reads only what the host routes.
	got, err := (hostnetwork.Host{Dir: "/nonexistent-astilba-state"}).Occupied(context.Background())
	if err != nil || !slices.Contains(got, netip.MustParsePrefix(os.Getenv("ASTILBA_HOST_RANGES_WANT"))) {
		t.Fatalf("occupied: %v %v", got, err)
	}
}
