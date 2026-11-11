package hostnetwork

import (
	"net/netip"
	"strings"
	"testing"
)

// nft refuses a transaction whose interval set has overlapping elements, which would leave
// a runner unable to build, so a range inside another with the same port is dropped.
func TestBuildRulesMergeNestedAllowances(t *testing.T) {
	rules, err := BuildRules(BuildPolicy{Subnet: netip.MustParsePrefix("10.252.0.0/25"), Allowed: []Destination{
		{netip.MustParsePrefix("10.0.4.12/32"), 443},
		{netip.MustParsePrefix("10.0.4.0/24"), 443},
		{netip.MustParsePrefix("10.0.4.0/24"), 443},
		{netip.MustParsePrefix("10.0.4.12/32"), 8080},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rules, "elements = { 10.0.4.0/24 . 443, 10.0.4.12/32 . 8080 }") {
		t.Fatal(rules)
	}
	if _, err = BuildRules(BuildPolicy{Subnet: netip.MustParsePrefix("203.0.113.0/25")}); err == nil {
		t.Fatal("translated a public range as the build network")
	}
}

// Builds can't use the host's loopback stub, so they get systemd-resolved's upstreams.
func TestBuildResolvers(t *testing.T) {
	stub := []byte("nameserver 127.0.0.53\noptions edns0\n")
	upstream := []byte("nameserver 213.186.33.99\nnameserver 169.254.169.254\nnameserver 2001:db8::1\nnameserver 10.0.0.2\nnameserver 10.0.0.3\n")
	got, err := buildResolvers(upstream, stub)
	if err != nil || len(got) != 3 || got[0].String() != "213.186.33.99" || got[1].String() != "169.254.169.254" || got[2].String() != "10.0.0.2" {
		t.Fatal(got, err)
	}
	got, err = buildResolvers(nil, stub)
	if err != nil || len(got) != 0 {
		t.Fatal("a loopback stub was given to builds", got, err)
	}
}
