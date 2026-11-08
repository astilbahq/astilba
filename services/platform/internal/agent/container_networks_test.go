package agent

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
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
	// 50 routed Compose environments with 21 networks each, plus ingress.
	got, ok := freeSubnets(DefaultNetworkPool, networkBits, nil, 50*21+1)
	if !ok || got[0] != netip.MustParsePrefix("10.252.0.0/25") || got[1] != netip.MustParsePrefix("10.252.0.128/25") {
		t.Fatalf("default pool: %d %t", len(got), ok)
	}
	for _, docker := range prefixes(t, "172.16.0.0/12", "192.168.0.0/16", "10.251.0.0/24") {
		if DefaultNetworkPool.Overlaps(docker) {
			t.Fatalf("default pool overlaps %s", docker)
		}
	}
	if err := ValidateNetworkPool(DefaultNetworkPool, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerPoolMustBePrivateLargeEnoughAndClearOfManagedNetworks(t *testing.T) {
	for _, ok := range []string{"10.0.0.0/8", "172.16.0.0/12", "172.20.0.0/16", "192.168.64.0/18"} {
		if err := ValidateNetworkPool(netip.MustParsePrefix(ok), prefixes(t, "192.168.200.0/24")); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	// Public, wider than a private range, too small, not written as a range, IPv6, unset.
	for _, bad := range []string{"100.64.0.0/10", "172.0.0.0/8", "192.168.0.0/19", "10.252.0.1/14", "fd00::/8"} {
		if ValidateNetworkPool(netip.MustParsePrefix(bad), nil) == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	if ValidateNetworkPool(netip.Prefix{}, nil) == nil {
		t.Fatal("unset pool accepted")
	}
	err := ValidateNetworkPool(netip.MustParsePrefix("10.0.0.0/8"), prefixes(t, "10.251.0.0/24"))
	if err == nil || !strings.Contains(err.Error(), "10.251.0.0/24") {
		t.Fatalf("pool over a managed network: %v", err)
	}
}

func TestFullPoolRefusalSaysWhatFilledIt(t *testing.T) {
	pool := netip.MustParsePrefix("10.252.0.0/24")
	// The owner's VPN routes all of 10.0.0.0/8: only another pool helps.
	covered := poolFull(pool, prefixes(t, "10.0.0.0/8"), nil)
	if !errors.Is(covered, errNetworkPoolFull) || !strings.Contains(covered.Error(), "cover the whole container network pool 10.252.0.0/24") || !strings.Contains(covered.Error(), "--network-pool") {
		t.Fatalf("covered: %v", covered)
	}
	// Networks used it up: the count tells running out from a misconfigured range.
	full := poolFull(pool, prefixes(t, "10.252.0.0/25"), prefixes(t, "10.252.0.128/25", "172.18.0.0/16"))
	if !errors.Is(full, errNetworkPoolFull) || !strings.Contains(full.Error(), "pool 10.252.0.0/24 has no free range left: 1 networks") {
		t.Fatalf("full: %v", full)
	}
}
