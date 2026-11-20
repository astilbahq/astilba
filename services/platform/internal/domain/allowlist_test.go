package domain

import (
	"strings"
	"testing"
)

func TestOwnerAllowlistAdmitsOnlyNarrowOrPrivateNetworks(t *testing.T) {
	for _, c := range []struct {
		name  string
		cidrs []string
		ok    bool
	}{
		{"empty", []string{}, false},
		{"every IPv4 address", []string{"0.0.0.0/0"}, false},
		{"every IPv6 address", []string{"::/0"}, false},
		{"public IPv4 /15", []string{"8.8.0.0/15"}, false},
		{"public IPv4 /16", []string{"8.8.0.0/16"}, true},
		{"one public address", []string{"203.0.113.7/32"}, true},
		{"RFC 1918 /8", []string{"10.0.0.0/8"}, true},
		{"carrier-grade NAT", []string{"100.64.0.0/10"}, true},
		{"RFC 1918 /12", []string{"172.16.0.0/12"}, true},
		{"RFC 1918 /16", []string{"192.168.0.0/16"}, true},
		{"IPv4 loopback", []string{"127.0.0.0/8"}, true},
		{"host bits set", []string{"203.0.113.5/0"}, false},
		{"host bits in a narrow range", []string{"203.0.113.5/24"}, false},
		{"spanning private and public", []string{"10.0.0.0/7"}, false},
		{"wider than its private range", []string{"172.0.0.0/8"}, false},
		{"public IPv6 /47", []string{"2001:db8::/47"}, false},
		{"public IPv6 /48", []string{"2001:db8::/48"}, true},
		{"unique local IPv6", []string{"fc00::/7"}, true},
		{"spanning unique local and public IPv6", []string{"fc00::/6"}, false},
		{"IPv4-mapped IPv6", []string{"::ffff:0:0/96"}, false},
		{"one bad entry among good", []string{"10.0.0.0/8", "0.0.0.0/0"}, false},
		{"not a prefix", []string{"everyone"}, false},
	} {
		if err := ValidateOwnerAllowlist(c.cidrs, nil); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// Management reads only masked prefixes from known sources, so a malformed report counts as no
// report rather than as a host with fewer ranges.
func TestParseContainerRangeRefusesWhatNoAgentSends(t *testing.T) {
	for _, c := range []struct {
		prefix, source string
		ok             bool
	}{
		{"10.252.0.0/14", "networkPool", true},
		{"fd00::/8", "addressPool", true},
		{"10.252.0.1/14", "networkPool", false},
		{"::ffff:10.0.0.0/104", "dockerNetwork", false},
		{"10.252.0.0/14", "elsewhere", false},
		{"pool", "networkPool", false},
	} {
		if _, ok := ParseContainerRange(c.prefix, c.source); ok != c.ok {
			t.Errorf("%s from %s: %v", c.prefix, c.source, ok)
		}
	}
}

// An IPv6 entry is never refused for an IPv4 range, or the other way round.
func TestContainerRangesOverlapOnlyTheirOwnFamily(t *testing.T) {
	pool, _ := ParseContainerRange("10.252.0.0/14", "networkPool")
	ula, _ := ParseContainerRange("fd00::/8", "addressPool")
	if err := ValidateOwnerAllowlist([]string{"fc00::/8", "192.168.1.0/24"}, []ContainerRange{pool, ula}); err != nil {
		t.Fatal(err)
	}
	if ValidateOwnerAllowlist([]string{"fd00:1::/32"}, []ContainerRange{pool, ula}) == nil {
		t.Fatal("admitted an IPv6 entry inside an IPv6 container range")
	}
}

// Every refusal fits the API's message length, even between the longest IPv6 prefixes, and one
// for a default address pool, which may hold no network yet, says how to narrow it.
func TestOverlapRefusalsFitTheAPI(t *testing.T) {
	longest := "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fff0/124"
	for source := range containerRangeSources {
		c, ok := ParseContainerRange(longest, source)
		if !ok {
			t.Fatal(source)
		}
		err := ValidateOwnerAllowlist([]string{longest}, []ContainerRange{c})
		if err == nil || len(err.Error()) > ownerAllowlistMessageLength || !strings.Contains(err.Error(), longest) {
			t.Errorf("%s: %v", source, err)
		}
		if source == "addressPool" && !strings.Contains(err.Error(), "default-address-pools") {
			t.Error("an address pool refusal does not say how to narrow the pool")
		}
	}
}
