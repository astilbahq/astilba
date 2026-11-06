package httpapi

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/auth"
)

func (s *Server) clientSourceKey(r *http.Request) string { return sourceKey(s.clientAddress(r)) }

func (s *Server) clientSource(r *http.Request) auth.Source {
	address := s.clientAddress(r)
	return auth.Source{Key: sourceKey(address), Network: sourceNetwork(address)}
}

// clientAddress trusts forwarding information only as far as the configured proxy chain,
// then takes the first untrusted hop.
func (s *Server) clientAddress(r *http.Request) netip.Addr {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	peer := remote.Addr().Unmap().WithZone("")
	client := peer
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		hops := strings.Split(values[i], ",")
		for j := len(hops) - 1; j >= 0; j-- {
			if !s.trustedProxy(client) {
				return client
			}
			next, err := netip.ParseAddr(strings.TrimSpace(hops[j]))
			if err != nil || next.Zone() != "" {
				// A broken chain cannot establish an address beyond the TCP peer.
				return peer
			}
			client = next.Unmap()
		}
	}
	return client
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

// sourceNetwork is the group a single client can cheaply rotate addresses within. Providers
// commonly give one customer an IPv6 /56 or /48, each holding many /64 budgets.
func sourceNetwork(address netip.Addr) string {
	if address.IsValid() && address.Is6() {
		return netip.PrefixFrom(address, 48).Masked().String()
	}
	return sourceKey(address)
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	for _, prefix := range s.TrustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
