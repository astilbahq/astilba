package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, peer, want string
		forwarded        []string
		trusted          []string
	}{
		{name: "direct ignores headers", peer: "192.0.2.10:1234", forwarded: []string{"198.51.100.20"}, want: "192.0.2.10"},
		{name: "unlisted proxy", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.11/32"}, forwarded: []string{"198.51.100.20"}, want: "192.0.2.10"},
		{name: "trusted proxy", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"198.51.100.20"}, want: "198.51.100.20"},
		{name: "client spoof is left of boundary", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"203.0.113.3, 198.51.100.20"}, want: "198.51.100.20"},
		{name: "malformed spoof is ignored", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"garbage, 198.51.100.20"}, want: "198.51.100.20"},
		{name: "two trusted proxies across header lines", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.0/24"}, forwarded: []string{"203.0.113.3, 198.51.100.20", "192.0.2.11"}, want: "198.51.100.20"},
		{name: "missing header", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, want: "192.0.2.10"},
		{name: "malformed trusted chain", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.0/24"}, forwarded: []string{"198.51.100.20, invalid, 192.0.2.11"}, want: "192.0.2.10"},
		{name: "empty hop", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.0/24"}, forwarded: []string{"198.51.100.20,"}, want: "192.0.2.10"},
		{name: "no ports in forwarded IPs", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"198.51.100.20:1234"}, want: "192.0.2.10"},
		{name: "no forwarded zone identifiers", peer: "192.0.2.10:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"fe80::1%eth0"}, want: "192.0.2.10"},
		{name: "IPv6 chain", peer: "[2001:db8:1::10]:1234", trusted: []string{"2001:db8:1::/64"}, forwarded: []string{" 2001:db8:2::20 , 2001:db8:1::11 "}, want: "2001:db8:2::20"},
		{name: "canonical IPv6", peer: "[2001:db8:1::10]:1234", trusted: []string{"2001:db8:1::/64"}, forwarded: []string{"2001:0db8:0002:0:0:0:0:0020"}, want: "2001:db8:2::20"},
		{name: "mapped IPv4 cannot reset budget", peer: "[::ffff:192.0.2.10]:1234", trusted: []string{"192.0.2.10/32"}, forwarded: []string{"::ffff:198.51.100.20"}, want: "198.51.100.20"},
		{name: "invalid peer never trusts forwarding", peer: "invalid", trusted: []string{"0.0.0.0/0"}, forwarded: []string{"198.51.100.20"}, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			for _, prefix := range tc.trusted {
				s.TrustedProxies = append(s.TrustedProxies, netip.MustParsePrefix(prefix))
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			r.Header["X-Forwarded-For"] = tc.forwarded
			r.Header.Set("Forwarded", "for=203.0.113.99")
			r.Header.Set("X-Real-IP", "203.0.113.99")
			r.Header.Set("CF-Connecting-IP", "203.0.113.99")
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}
