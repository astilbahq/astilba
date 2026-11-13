package domain

import "testing"

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
		{"host bits set", []string{"203.0.113.5/0"}, false},
		{"host bits in a narrow range", []string{"203.0.113.5/24"}, false},
		{"spanning private and public", []string{"10.0.0.0/7"}, false},
		{"wider than its private range", []string{"172.0.0.0/8"}, false},
		{"public IPv6 /47", []string{"2001:db8::/47"}, false},
		{"public IPv6 /48", []string{"2001:db8::/48"}, true},
		{"unique local IPv6", []string{"fc00::/7"}, true},
		{"IPv4-mapped IPv6", []string{"::ffff:0:0/96"}, false},
		{"one bad entry among good", []string{"10.0.0.0/8", "0.0.0.0/0"}, false},
		{"not a prefix", []string{"everyone"}, false},
	} {
		if err := ValidateOwnerAllowlist(c.cidrs); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
