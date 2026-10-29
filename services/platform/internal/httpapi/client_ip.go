package httpapi

import (
	"net/http"
	"net/netip"
	"strings"
)

// clientSourceKey trusts forwarding information only as far as the configured
// proxy chain, then groups the first untrusted hop into its rate-limit budget.
func (s *Server) clientSourceKey(r *http.Request) string {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return sourceKey(netip.Addr{})
	}
	peer := remote.Addr().Unmap().WithZone("")
	client := peer
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		hops := strings.Split(values[i], ",")
		for j := len(hops) - 1; j >= 0; j-- {
			if !s.trustedProxy(client) {
				return sourceKey(client)
			}
			next, err := netip.ParseAddr(strings.TrimSpace(hops[j]))
			if err != nil || next.Zone() != "" {
				// A broken chain cannot establish an address beyond the TCP peer.
				return sourceKey(peer)
			}
			client = next.Unmap()
		}
	}
	return sourceKey(client)
}

func sourceKey(address netip.Addr) string {
	if !address.IsValid() {
		return "unknown"
	}
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	for _, prefix := range s.TrustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
