//go:build linux

package hostnetwork

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/host"
	"github.com/google/uuid"
)

func TestNativeEndpointHelper(t *testing.T) {
	if os.Getenv("ASTILBA_NETWORK_ENDPOINT_HELPER") != "1" {
		t.Skip("network namespace helper")
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "private-endpoint-canary") })
	if err := http.ListenAndServe("10.203.0.2:8080", nil); err != nil {
		t.Fatal(err)
	}
}

func TestNativeManagedLinks(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_NETWORK") != "1" {
		t.Skip("requires an explicitly disposable Ubuntu networking host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root required in the owned disposable fixture")
	}
	if os.Getenv("ASTILBA_NETWORK_SERVICE_FIXTURE") != "1" {
		testNetworkService(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	owner := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture %s: %s (%v)", args[0], out, err)
		}
		return out
	}
	ns := map[string]string{}
	for _, key := range []string{"a", "b", "u", "v", "d", "forged", "ingress"} {
		name := "astilba-" + owner + "-" + key
		ns[key] = name
		run("ip", "netns", "add", name)
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "delete", name).Run() })
		run("ip", "-n", name, "link", "set", "lo", "up")
	}
	in := func(key string, args ...string) []byte {
		return run(append([]string{"ip", "netns", "exec", ns[key]}, args...)...)
	}
	link := func(key, other, name, otherName, address, otherAddress string) {
		run("ip", "link", "add", name, "type", "veth", "peer", "name", otherName)
		run("ip", "link", "set", name, "netns", ns[key])
		run("ip", "link", "set", otherName, "netns", ns[other])
		in(key, "ip", "addr", "add", address, "dev", name)
		in(other, "ip", "addr", "add", otherAddress, "dev", otherName)
		in(key, "ip", "link", "set", name, "up")
		in(other, "ip", "link", "set", otherName, "up")
	}
	link("a", "b", "under-a", "under-b", "192.0.2.1/24", "192.0.2.2/24")
	bridge := func(host, client, bridge, subnet, clientAddr string) {
		in(host, "ip", "link", "add", bridge, "type", "bridge")
		in(host, "ip", "addr", "add", subnet+".1/24", "dev", bridge)
		in(host, "ip", "link", "set", bridge, "up")
		hostEnd, clientEnd := "h-"+client, "c-"+client
		link(host, client, hostEnd, clientEnd, subnet+".254/24", clientAddr+"/24")
		in(host, "ip", "addr", "flush", "dev", hostEnd)
		in(host, "ip", "link", "set", hostEnd, "master", bridge)
		in(client, "ip", "route", "add", "default", "via", subnet+".1")
	}
	bridge("a", "u", "br-111111111111", "10.201.0", "10.201.0.2")
	bridge("a", "v", "br-222222222222", "10.202.0", "10.202.0.2")
	bridge("b", "d", "br-333333333333", "10.203.0", "10.203.0.2")
	bridge("a", "ingress", "br-444444444444", "10.205.0", "10.205.0.2")
	link("b", "forged", "plain-b", "plain-f", "10.204.0.1/24", "10.204.0.2/24")
	for _, key := range []string{"a", "b"} {
		in(key, "sysctl", "-q", "-w", "net.ipv4.ip_forward=1")
		in(key, "sysctl", "-q", "-w", "net.ipv4.conf.all.rp_filter=0")
	}
	host := func(key string) Host {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		h := Host{Dir: dir}
		for _, tool := range []string{"wg", "ip", "nft"} {
			path := filepath.Join(dir, tool)
			script := "#!/bin/sh\n/usr/sbin/ip netns exec " + ns[key] + " " + tool + " \"$@\" 2>>" + filepath.Join(dir, "tool-errors") + "\nstatus=$?\nif [ $status -ne 0 ]; then echo \"" + tool + " $*\" >>" + filepath.Join(dir, "tool-errors") + "; fi\nexit $status\n"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			switch tool {
			case "wg":
				h.WG = path
			case "ip":
				h.IP = path
			case "nft":
				h.NFT = path
			}
		}
		return h
	}
	a, b := host("a"), host("b")
	if !a.Supported(ctx) || !b.Supported(ctx) {
		t.Fatal("pinned tools, kernel or installed-service network permissions unavailable")
	}
	ma, mb := fixture(t), fixture(t)
	network := uuid.New()
	ma.NetworkID = network
	mb.NetworkID = network
	ma.Address = "10.251.0.1"
	mb.Address = "10.251.0.2"
	ma.MembershipID = uuid.New()
	mb.MembershipID = uuid.New()
	ka, err := a.Identity(ma.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.Identity(mb.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	ma.PublicKey = ka.PublicKey
	mb.PublicKey = kb.PublicKey
	ma.Peers = []Peer{{ServerID: mb.ServerID, MembershipID: mb.MembershipID, Address: mb.Address, PublicKey: mb.PublicKey, Endpoint: "192.0.2.2:51820"}}
	mb.Peers = []Peer{{ServerID: ma.ServerID, MembershipID: ma.MembershipID, Address: ma.Address, PublicKey: ma.PublicKey, Endpoint: "192.0.2.1:51820"}}
	unitA, unitB := uuid.New(), uuid.New()
	endpoint := Endpoint{ID: uuid.New(), UnitID: unitB, ResourceID: uuid.New(), InstanceID: uuid.New(), Port: 20001, ContainerPort: 8080, Sources: []Source{{ServerID: ma.ServerID, UnitID: unitA}}}
	ma.Exports = nil
	ma.Egress = []Egress{{Ingress: true, UnitID: unitA, ServerID: mb.ServerID, EndpointID: endpoint.ID, Port: endpoint.Port}}
	mb.Exports = []Endpoint{endpoint}
	mb.Egress = nil
	ba := []Bridge{{UnitID: unitA, Name: "br-111111111111", Sources: []string{"10.201.0.0/24"}, Proxy: []ProxyPath{{Name: "br-444444444444", Prefix: "10.205.0.0/24"}}}}
	bb := []Bridge{{UnitID: unitB, Name: "br-333333333333", Sources: []string{"10.203.0.0/24"}}}
	// These NAT chains reproduce Docker port-publishing hook priorities.
	natA := fmt.Sprintf("table ip fixture {\n chain post { type nat hook postrouting priority 100; policy accept; oifname %q ip saddr { 10.201.0.0/24,10.202.0.0/24,10.205.0.0/24 } masquerade;\n }\n}\n", ma.Interface())
	natB := fmt.Sprintf("table ip fixture {\n chain pre { type nat hook prerouting priority -100; policy accept; ip daddr %s tcp dport 20001 dnat to 10.203.0.2:8080;\n }\n}\n", mb.Address)
	for key, raw := range map[string]string{"a": natA, "b": natB} {
		cmd := exec.CommandContext(ctx, "ip", "netns", "exec", ns[key], "nft", "-f", "-")
		cmd.Stdin = strings.NewReader(raw)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture NAT %s: %s %v", key, out, err)
		}
	}
	// Inject a lost response after atomic create+ownership marker. Retry must recognize its
	// owned interface and finish its alias without adopting unrelated state.
	originalIP := a.IP
	marker := filepath.Join(root, "create-once")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1 $2\" = 'link add' ] && [ ! -e %s ]; then\n touch %s\n %s \"$@\"\n exit 1\nfi\nexec %s \"$@\"\n", marker, marker, originalIP, originalIP)
	a.IP = filepath.Join(root, "interrupt-ip")
	if err := os.WriteFile(a.IP, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = a.Apply(ctx, ma, ba, false); err == nil {
		t.Fatal("fault injection did not interrupt interface creation")
	}
	a.IP = originalIP
	if err = a.Apply(ctx, ma, ba, false); err != nil {
		diagnostic, _ := os.ReadFile(filepath.Join(a.Dir, "tool-errors"))
		t.Fatalf("owned create retry: %v; %s", err, diagnostic)
	}
	if err = b.Apply(ctx, mb, bb, false); err != nil {
		t.Fatal("destination apply", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.CommandContext(ctx, "ip", "netns", "exec", ns["d"], executable, "-test.run=^TestNativeEndpointHelper$")
	helper.Env = append(os.Environ(), "ASTILBA_NETWORK_ENDPOINT_HELPER=1")
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	url := "http://" + mb.Address + ":20001/"
	request := func(key string, extra ...string) ([]byte, error) {
		args := []string{"netns", "exec", ns[key]}
		args = append(args, extra...)
		args = append(args, "curl", "--noproxy", "*", "--max-time", "2", "--fail", "--silent", url)
		return exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		out, err := request("u")
		if err == nil && strings.Contains(string(out), "private-endpoint-canary") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("authorized unit cannot reach endpoint: %s %v", out, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out, err := request("ingress"); err != nil || !strings.Contains(string(out), "private-endpoint-canary") {
		t.Fatalf("dedicated ingress path unavailable: %s %v", out, err)
	}
	for _, key := range []string{"v", "a"} {
		var err error
		if key == "a" {
			_, err = request(key, "setpriv", "--reuid=65534", "--regid=65534", "--clear-groups")
		} else {
			_, err = request(key)
		}
		if err == nil {
			t.Fatalf("ungranted source %s reached private endpoint", key)
		}
	}
	in("forged", "ip", "address", "add", ma.Address+"/32", "dev", "plain-f")
	in("forged", "ip", "route", "add", mb.Address+"/32", "via", "10.204.0.1")
	forged := exec.CommandContext(ctx, "ip", "netns", "exec", ns["forged"], "curl", "--noproxy", "*", "--interface", ma.Address, "--max-time", "2", "--fail", "--silent", url)
	if out, err := forged.CombinedOutput(); err == nil {
		t.Fatalf("wrong-interface forged peer reached endpoint: %s", out)
	}
	// Removing the management process does not remove approved links; restore
	// reconstructs owned interfaces from host-local state without controller input.
	for key, h := range map[string]Host{"a": a, "b": b} {
		m := ma
		if key == "b" {
			m = mb
		}
		in(key, "ip", "link", "delete", m.Interface())
		if err = h.Restore(ctx); err != nil {
			t.Fatal("restart restore", err)
		}
	}
	if out, err := request("u"); err != nil || !strings.Contains(string(out), "private-endpoint-canary") {
		t.Fatal("restored link unavailable", err)
	}
	next := mb
	next.Generation++
	next.Exports = nil
	originalNFT := b.NFT
	fail := filepath.Join(root, "nft-once")
	b.NFT = filepath.Join(root, "interrupt-nft")
	script = fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = '-f' ] && [ ! -e %s ]; then touch %s; exit 1; fi\nexec %s \"$@\"\n", fail, fail, originalNFT)
	if err = os.WriteFile(b.NFT, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = b.Apply(ctx, next, bb, true); err == nil {
		t.Fatal("restriction fault did not occur")
	}
	b.NFT = originalNFT
	if err = b.Apply(ctx, next, bb, true); err != nil {
		t.Fatal("same restriction retry", err)
	}
	if _, err = request("u"); err == nil {
		t.Fatal("revoked grant remained reachable")
	}
	if err = b.Apply(ctx, next, bb, true); err != nil {
		t.Fatal("lost receipt restriction retry", err)
	}
	for _, key := range []string{"a", "b"} {
		raw := in(key, "nft", "-j", "list", "table", "ip", "fixture")
		if !json.Valid(raw) {
			t.Fatal("unrelated firewall table was lost")
		}
	}
}

func testNetworkService(t *testing.T) {
	t.Helper()
	owner := uuid.NewString()
	root := "/opt/astilba-network-test-" + owner
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "test")
	if err = os.WriteFile(binaryPath, raw, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll("/run/netns", 0755); err != nil {
		t.Fatal(err)
	}
	text := string(host.NetworkService())
	text = strings.Replace(text, "ExecStart=/usr/local/bin/astilba-agent network-restore", "Environment=ASTILBA_NATIVE_NETWORK=1 ASTILBA_NETWORK_SERVICE_FIXTURE=1\nExecStart="+binaryPath+" -test.v -test.run=^TestNativeManagedLinks$", 1)
	text = strings.Replace(text, "ReadWritePaths=/var/lib/astilba-agent", "ReadWritePaths="+root+" /run/netns", 1)
	unit := "astilba-network-test-" + owner + ".service"
	path := filepath.Join("/run/systemd/system", unit)
	if err = os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "stop", unit).Run()
		if raw, err := os.ReadFile(path); err == nil && string(raw) == text {
			_ = os.Remove(path)
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
	})
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("fixture unit reload: %s %v", out, err)
	}
	out, err := exec.Command("systemctl", "start", unit).CombinedOutput()
	logs, _ := exec.Command("journalctl", "-u", unit, "--no-pager", "-n", "100").CombinedOutput()
	if err != nil {
		t.Fatalf("installed network service: %s %s %v", out, logs, err)
	}
	if !strings.Contains(string(logs), "--- PASS: TestNativeManagedLinks") {
		t.Fatalf("missing installed-service packet evidence: %s", logs)
	}
}
