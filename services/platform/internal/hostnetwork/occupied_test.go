package hostnetwork

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestOccupiedReadsEveryRouteThatNamesANetwork(t *testing.T) {
	// Captured from `ip -j -4 route show table all` with a gateway route, an unreachable
	// route, a route in another table, a VPN's default replacement and local addresses.
	raw := []byte(`[
{"dst":"default","gateway":"192.168.5.2","dev":"eth0","protocol":"dhcp","prefsrc":"192.168.5.32","metric":200,"flags":[]},
{"dst":"0.0.0.0/1","gateway":"10.8.0.1","dev":"tun0","flags":[]},
{"dst":"128.0.0.0/1","gateway":"10.8.0.1","dev":"tun0","flags":[]},
{"dst":"172.17.0.0/16","dev":"docker0","protocol":"kernel","scope":"link","prefsrc":"172.17.0.1","flags":["linkdown"]},
{"dst":"10.40.0.0/16","gateway":"192.168.5.2","dev":"eth0","flags":[]},
{"type":"unreachable","dst":"10.252.9.0/24","flags":[]},
{"dst":"100.101.102.103","dev":"tailscale0","table":"52","flags":[]},
{"type":"local","dst":"192.168.5.32","table":"local","dev":"eth0","protocol":"kernel","scope":"host","prefsrc":"192.168.5.32","flags":[]},
{"type":"broadcast","dst":"192.168.5.255","table":"local","dev":"eth0","flags":[]},
{"dst":"not-an-address","dev":"x","flags":[]}
]`)
	routes, err := parseRoutes(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolv := []byte("# comment\nnameserver 127.0.0.53\nnameserver 10.252.7.2\nnameserver fd00::1\nsearch example.test\n")
	got := occupiedFrom(routes, resolv)
	want := []netip.Prefix{}
	for _, v := range []string{"172.17.0.0/16", "10.40.0.0/16", "10.252.9.0/24", "100.101.102.103/32", "192.168.5.32/32", "192.168.5.255/32", "10.252.7.2/32"} {
		want = append(want, netip.MustParsePrefix(v))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if _, err = parseRoutes([]byte("not json")); err == nil {
		t.Fatal("unreadable routes accepted")
	}
}

func TestManagedPrefixesIncludeEveryHeldNetwork(t *testing.T) {
	h := Host{Dir: t.TempDir()}
	if got, err := h.ManagedPrefixes(); err != nil || len(got) != 0 {
		t.Fatalf("no network state: %v %v", got, err)
	}
	m := Manifest{Version: Version, NetworkID: uuid.New(), MembershipID: uuid.New(), ServerID: uuid.New(), Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.2", PublicKey: "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", ListenPort: 51820, Peers: []Peer{}, Exports: []Endpoint{}, Egress: []Egress{}}
	if err := os.MkdirAll(filepath.Join(h.Dir, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.save(Snapshot{Manifest: m, Bridges: []Bridge{}}); err != nil {
		t.Fatal(err)
	}
	if got, err := h.ManagedPrefixes(); err != nil || !slices.Equal(got, []netip.Prefix{netip.MustParsePrefix("10.251.0.0/24")}) {
		t.Fatalf("got %v %v", got, err)
	}
	// State the agent can't read leaves the prefix unknown, so nothing may be placed.
	raw, _ := stablejson.Marshal(map[string]string{"unexpected": "state"})
	if err := os.WriteFile(filepath.Join(h.Dir, "network", uuid.NewString()+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ManagedPrefixes(); err == nil {
		t.Fatal("unreadable managed network state ignored")
	}
}
