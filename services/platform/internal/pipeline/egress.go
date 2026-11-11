package pipeline

import (
	"cmp"
	"errors"
	"net/netip"
	"slices"
)

// MaxEgressRules bounds a runner's build network allowlist.
const MaxEgressRules = 32

// EgressRule lets builds on one runner open TCP connections to Port on Prefix, a private
// IPv4 range the build network otherwise refuses, such as a package mirror on the LAN.
type EgressRule struct {
	Prefix string `json:"prefix"`
	Port   int    `json:"port"`
}

// AllowableRanges are the private and shared IPv4 ranges a build reaches only through an
// owner's allowlist: RFC 1918, carrier-grade NAT (also used by Tailscale and some clouds'
// metadata), IETF protocol assignments and benchmarking.
var AllowableRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
}

// NeverAllowed are the IPv4 destinations no allowlist can open to a build: "this network",
// loopback, link-local, which holds most clouds' instance metadata, Alibaba Cloud's metadata,
// Azure's host endpoint (WireServer), multicast and reserved.
var NeverAllowed = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// ParseEgressRule reads an owner's entry, an IPv4 address or range and a TCP port.
func ParseEgressRule(prefix string, port int) (EgressRule, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		a, e := netip.ParseAddr(prefix)
		if e != nil {
			return EgressRule{}, errors.New("invalid address")
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	rule := EgressRule{Prefix: p.Masked().String(), Port: port}
	return rule, rule.validate()
}

func (r EgressRule) validate() error {
	p, err := netip.ParsePrefix(r.Prefix)
	if err != nil || !p.Addr().Is4() || p != p.Masked() || p.String() != r.Prefix {
		return errors.New("an allowed build destination must be a canonical IPv4 address or range")
	}
	if r.Port < 1 || r.Port > 65535 {
		return errors.New("an allowed build destination needs a TCP port from 1 to 65535")
	}
	if slices.ContainsFunc(NeverAllowed, p.Overlaps) {
		return errors.New("loopback, link-local (including cloud instance metadata) and multicast addresses can't be allowed")
	}
	if !slices.ContainsFunc(AllowableRanges, func(r netip.Prefix) bool { return r.Bits() <= p.Bits() && r.Contains(p.Addr()) }) {
		return errors.New("only private addresses need allowing: builds already reach public addresses")
	}
	return nil
}

// CanonicalEgress returns rules sorted and without duplicates, as stored and frozen.
func CanonicalEgress(rules []EgressRule) []EgressRule {
	out := slices.Clone(rules)
	slices.SortFunc(out, func(a, b EgressRule) int { return cmp.Or(cmp.Compare(a.Prefix, b.Prefix), cmp.Compare(a.Port, b.Port)) })
	return slices.Compact(out)
}

// ValidateEgress checks a canonical allowlist.
func ValidateEgress(rules []EgressRule) error {
	if len(rules) > MaxEgressRules {
		return errors.New("a runner allows at most 32 private build destinations")
	}
	if !slices.Equal(rules, CanonicalEgress(rules)) {
		return errors.New("build destinations must be sorted and distinct")
	}
	for _, r := range rules {
		if err := r.validate(); err != nil {
			return err
		}
	}
	return nil
}

// EgressCovers reports whether every rule in frozen is still allowed by a rule in current,
// on the same port and a range containing it, so a build queued before the owner removed a
// destination can't reach it, and one queued before a wider entry replaced it still runs.
func EgressCovers(current, frozen []EgressRule) bool {
	for _, f := range frozen {
		inner, err := netip.ParsePrefix(f.Prefix)
		if err != nil {
			return false
		}
		if !slices.ContainsFunc(current, func(c EgressRule) bool {
			outer, err := netip.ParsePrefix(c.Prefix)
			return err == nil && c.Port == f.Port && outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
		}) {
			return false
		}
	}
	return true
}
