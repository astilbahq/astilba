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
	// /proc/net/fib_trie from Ubuntu 24.04 with a gateway route, an unreachable route, a
	// Tailscale-style route in table 52 and a VPN's default replacement in table 77.
	raw := []byte(`Id 52:
  |-- 100.101.102.103
     /32 link UNICAST
Id 77:
  |-- 0.0.0.0
     /1 universe UNICAST
Main:
  +-- 0.0.0.0/0 3 0 4
     +-- 0.0.0.0/4 2 0 2
        |-- 0.0.0.0
           /0 universe UNICAST
        +-- 10.0.0.0/8 2 0 2
           |-- 10.40.0.0
              /16 universe UNICAST
           |-- 10.252.9.0
              /24 universe UNREACHABLE
     +-- 172.16.0.0/14 3 0 6
        +-- 172.17.0.0/31 1 0 0
           |-- 172.17.0.0
              /16 link UNICAST
           |-- 172.17.0.1
              /32 host LOCAL
        |-- 172.17.255.255
           /32 link BROADCAST
     +-- 192.168.5.0/24 2 1 2
        |-- 192.168.5.32
           /32 host LOCAL
           /24 link UNICAST
`)
	routes, err := parseFIB(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolv := []byte("# comment\nnameserver 127.0.0.53\nnameserver 10.252.7.2\nnameserver fd00::1\nsearch example.test\n")
	got := occupiedFrom(routes, resolv)
	want := []netip.Prefix{}
	for _, v := range []string{"100.101.102.103/32", "10.40.0.0/16", "10.252.9.0/24", "172.17.0.0/16", "172.17.0.1/32", "172.17.255.255/32", "192.168.5.32/32", "192.168.5.0/24", "10.252.7.2/32"} {
		want = append(want, netip.MustParsePrefix(v))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	// A list the agent can't read could hide a route, so nothing may be placed.
	for _, bad := range []string{"Main:\n     /24 link UNICAST\n", "Main:\n  |-- not-an-address\n", "Main:\n  |-- 10.0.0.0\n     /33 link UNICAST\n"} {
		if _, err = parseFIB([]byte(bad)); err == nil {
			t.Fatalf("unreadable routes accepted: %q", bad)
		}
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
