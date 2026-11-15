package domain

import "net/netip"

// privateClientRanges are the networks a client allowlist may name at any width: RFC 1918,
// carrier-grade NAT, IPv4 loopback and IPv6 unique local addresses. The internet does not
// route them, so naming one admits no stranger. IPv6 loopback is a /128, narrow enough anyway.
var privateClientRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// Public prefixes may be no wider than these: a provider's block, not the internet.
const (
	widestPublicIPv4 = 16
	widestPublicIPv6 = 48
)

// ValidateOwnerAllowlist refuses a client allowlist that would admit more than its owner's
// networks: none at all, an entry with host bits set (203.0.113.5/0 reads as one address but
// admits every IPv4 one), an IPv4-mapped IPv6 range (::ffff:0:0/96 admits every IPv4 client
// within the IPv6 limit), or a public range wider than /16 for IPv4 or /48 for IPv6. A range
// counts as private only when it lies wholly inside one private range. Staging an application
// whose first account becomes its administrator needs this; widening the route afterwards is
// an ordinary route change.
func ValidateOwnerAllowlist(cidrs []string) error {
	refuse := &Error{Code: "invalid_input", Message: "Allow only your own networks until you have created the application's administrator account: no public range wider than /16 (IPv4) or /48 (IPv6), written without host bits. Widen the route after that.", Fields: []string{"route.allowCidrs"}}
	if len(cidrs) == 0 {
		return refuse
	}
	for _, raw := range cidrs {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p != p.Masked() || p.Addr().Is4In6() {
			return refuse
		}
		private := false
		for _, r := range privateClientRanges {
			// Contains never matches across address families.
			private = private || r.Bits() <= p.Bits() && r.Contains(p.Addr())
		}
		widest := widestPublicIPv6
		if p.Addr().Is4() {
			widest = widestPublicIPv4
		}
		if !private && p.Bits() < widest {
			return refuse
		}
	}
	return nil
}
