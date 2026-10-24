package httpapi

import (
	"net/http"
	"net/netip"
	"strings"
)

// clientIP trusts forwarding information only as far as the configured proxy
// chain. The first untrusted hop from the right owns the rate-limit budget.
func (s *Server) clientIP(r *http.Request) string {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peer := remote.Addr().Unmap().WithZone("")
	client := peer
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		hops := strings.Split(values[i], ",")
		for j := len(hops) - 1; j >= 0; j-- {
			if !s.trustedProxy(client) {
				return client.String()
			}
			next, err := netip.ParseAddr(strings.TrimSpace(hops[j]))
			if err != nil || next.Zone() != "" {
				// A broken chain cannot establish an address beyond the TCP peer.
				return peer.String()
			}
			client = next.Unmap()
		}
	}
	return client.String()
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	for _, prefix := range s.TrustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
