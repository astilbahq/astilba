package agent

import (
	"net/netip"
	"slices"
	"testing"
)

func prefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	out := []netip.Prefix{}
	for _, v := range values {
		out = append(out, netip.MustParsePrefix(v))
	}
	return out
}

func TestFreeSubnetsSkipEverythingOccupied(t *testing.T) {
	occupied := prefixes(t,
		"10.252.0.0/24",   // an existing network
		"10.252.1.128/25", // part of a /24 is enough to rule it out
		"10.252.2.53/32",  // a nameserver
		"10.252.4.0/23",   // a LAN wider than one subnet
		"10.0.0.0/16",     // outside the pool
	)
	got, ok := freeSubnets(netip.MustParsePrefix("10.252.0.0/14"), 24, occupied, 3)
	if !ok || !slices.Equal(got, prefixes(t, "10.252.3.0/24", "10.252.6.0/24", "10.252.7.0/24")) {
		t.Fatalf("got %v %t", got, ok)
	}
}

func TestFreeSubnetsRefuseWhenThePoolCannotHoldEveryRequest(t *testing.T) {
	pool := netip.MustParsePrefix("10.252.0.0/22")
	if got, ok := freeSubnets(pool, 24, prefixes(t, "10.252.1.0/24"), 3); !ok || len(got) != 3 {
		t.Fatalf("three of four free: %v %t", got, ok)
	}
	// The plan needs four, so none is offered: nothing is created for a plan that can't fit.
	if _, ok := freeSubnets(pool, 24, prefixes(t, "10.252.1.0/24"), 4); ok {
		t.Fatal("offered subnets for a plan the pool cannot hold")
	}
	// A route covering the whole pool, such as the owner's 10.0.0.0/8 VPN, fills it.
	if _, ok := freeSubnets(pool, 24, prefixes(t, "10.0.0.0/8"), 1); ok {
		t.Fatal("offered a subnet inside a routed range")
	}
}

func TestDefaultPoolHoldsEveryNetworkTheHostCapsAllow(t *testing.T) {
	// 1,024 networks: 50 routed environments with 20 networks each, plus ingress.
	got, ok := freeSubnets(DefaultNetworkPool, networkBits, nil, 1024)
	if !ok || got[0] != netip.MustParsePrefix("10.252.0.0/24") || got[1023] != netip.MustParsePrefix("10.255.255.0/24") {
		t.Fatalf("default pool: %d %t", len(got), ok)
	}
	if _, ok = freeSubnets(DefaultNetworkPool, networkBits, nil, 1025); ok {
		t.Fatal("default pool offered more than it holds")
	}
	for _, docker := range prefixes(t, "172.16.0.0/12", "192.168.0.0/16", "10.251.0.0/24") {
		if DefaultNetworkPool.Overlaps(docker) {
			t.Fatalf("default pool overlaps %s", docker)
		}
	}
}
