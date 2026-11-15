package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/api/types/system"
)

// reported reads the ranges back as management does.
func reported(t *testing.T, ranges []api.ContainerRange) []domain.ContainerRange {
	t.Helper()
	out := []domain.ContainerRange{}
	for _, r := range ranges {
		c, ok := domain.ParseContainerRange(r.Prefix, string(r.Source))
		if !ok {
			t.Fatalf("management would not read %+v", r)
		}
		out = append(out, c)
	}
	return out
}

// A route allowlist that overlaps any range the agent reports is refused, naming that range,
// whichever source it comes from; one naming only the owner's own address passes.
func TestReportedContainerRangesRefuseOverlappingAllowlists(t *testing.T) {
	pools := addressPools([]system.NetworkAddressPool{{Base: netip.MustParsePrefix("10.200.0.0/16"), Size: 24}, {Base: netip.MustParsePrefix("fd12:3456::/48"), Size: 64}})
	ranges, err := collectContainerRanges(DefaultNetworkPool, prefixes(t, "172.17.0.0/16"), pools, prefixes(t, "10.251.0.0/24"), prefixes(t, "192.168.50.0/24", "10.252.0.0/25"))
	if err != nil {
		t.Fatal(err)
	}
	host := reported(t, ranges)
	for _, c := range []struct {
		name, entry, overlaps string
	}{
		{"the agent's network pool", "10.0.0.0/8", "10.252.0.0/14"},
		{"Docker's default bridge", "172.17.5.0/24", "172.17.0.0/16"},
		{"a configured address pool", "10.200.7.0/24", "10.200.0.0/16"},
		{"an IPv6 address pool", "fd12:3456:0:1::/64", "fd12:3456::/48"},
		{"an existing Docker network", "192.168.0.0/16", "192.168.50.0/24"},
		{"a managed network", "10.251.0.0/16", "10.251.0.0/24"},
	} {
		err := domain.ValidateOwnerAllowlist([]string{"203.0.113.7/32", c.entry}, host)
		if invalid := (*domain.Error)(nil); !errors.As(err, &invalid) || !strings.Contains(invalid.Message, c.entry) || !strings.Contains(invalid.Message, c.overlaps) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := domain.ValidateOwnerAllowlist([]string{"203.0.113.7/32", "2001:db8::/64", "10.100.0.0/16"}, host); err != nil {
		t.Fatal("refused an allowlist of the owner's own networks:", err)
	}
}

func TestContainerRangesListEachRangeOnce(t *testing.T) {
	// The agent's own networks lie in its pool, and a Docker network in a default pool is in it.
	ranges, err := collectContainerRanges(netip.MustParsePrefix("10.100.0.0/16"), prefixes(t, "172.17.0.0/16"), addressPools(nil), nil, prefixes(t, "10.100.3.0/25", "172.18.0.0/16", "172.20.4.0/24", "10.0.0.0/8"))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range ranges {
		got = append(got, r.Prefix+" "+string(r.Source))
	}
	// A wider Docker network replaces the pool it holds. Docker's built-in pools are reported
	// when daemon.json sets none, and an IPv6 range for the one Docker derives.
	want := "172.17.0.0/16 defaultBridge, 172.18.0.0/16 addressPool, 172.19.0.0/16 addressPool, 172.20.0.0/14 addressPool, 172.24.0.0/14 addressPool, 172.28.0.0/14 addressPool, 192.168.0.0/16 addressPool, fd00::/8 addressPool, 10.0.0.0/8 dockerNetwork"
	if strings.Join(got, ", ") != want {
		t.Fatalf("got %s", strings.Join(got, ", "))
	}
	// Configured IPv4 pools replace Docker's defaults; an IPv6 one is still derived.
	if got := addressPools([]system.NetworkAddressPool{{Base: netip.MustParsePrefix("10.200.0.0/16"), Size: 24}}); len(got) != 2 || got[0].String() != "10.200.0.0/16" || got[1] != dockerDerivedULA {
		t.Fatal("configured pools", got)
	}
}

// A host with more ranges than a heartbeat carries reports none, which management reads as
// unknown, rather than a list missing some.
func TestTooManyContainerRangesReportNone(t *testing.T) {
	networks := []netip.Prefix{}
	for i := range maxContainerRanges {
		networks = append(networks, netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, byte(i), 0}), 24))
	}
	if _, err := collectContainerRanges(DefaultNetworkPool, nil, addressPools(nil), nil, networks); !errors.Is(err, errTooManyContainerRanges) {
		t.Fatal(err)
	}
}

// The ranges are a negotiated extension: a controller that doesn't read them never receives them,
// in capabilities or preflight, since an older one refuses the unknown members.
func TestContainerRangesWaitForTheirNegotiation(t *testing.T) {
	j, _ := testVisualJournal(t)
	var offered atomic.Bool
	var received, rejected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Capabilities map[string]jsontext.Value `json:"capabilities"`
			Preflight    map[string]jsontext.Value `json:"preflight"`
		}
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		if offered.Load() {
			w.Header().Set("Astilba-Agent-Container-Ranges", "v1")
		}
		_, capability := body.Capabilities["containerRanges"]
		_, ranges := body.Preflight["containerRanges"]
		if capability || ranges {
			received.Add(1)
			if !offered.Load() || !capability || !ranges {
				rejected.Add(1)
				w.WriteHeader(400)
				return
			}
		}
		_, _ = w.Write([]byte(`{"role":"combined","rotationRequested":false}`))
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin = server.URL
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP = server.Client()
	yes := true
	ranges := []api.ContainerRange{{Prefix: DefaultNetworkPool.String(), Source: api.ContainerRangeSourceNetworkPool}}
	heartbeat := func() error {
		return c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{Capabilities: api.AgentCapabilities{ContainerRanges: &yes}, Preflight: api.HostPreflight{Notes: []string{}, ContainerRanges: &ranges}}, &api.AgentStatus{}, "fixture")
	}
	for range 2 {
		if err := heartbeat(); err != nil || received.Load() != 0 {
			t.Fatal("a controller that doesn't read container ranges received them", err)
		}
	}
	offered.Store(true)
	for range 2 {
		if err := heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 1 || rejected.Load() != 0 {
		t.Fatalf("ranges sent %d times after negotiation, %d rejected", received.Load(), rejected.Load())
	}
}

// The note about unread ranges speaks of a check only a controller that reads them makes.
func TestUnreadRangesNoteWaitsForItsNegotiation(t *testing.T) {
	j, _ := testVisualJournal(t)
	c := NewClient(j, nil)
	in := api.HostPreflight{Notes: []string{"Existing containers stay unmanaged.", containerRangesUnreadNote}}
	if got := c.advertisedPreflight(in).Notes; len(got) != 1 || len(in.Notes) != 2 {
		t.Fatal("a controller that doesn't read ranges was told they went unread:", got)
	}
	c.containerRanges.Store(true)
	if got := c.advertisedPreflight(in).Notes; len(got) != 2 {
		t.Fatal(got)
	}
}
