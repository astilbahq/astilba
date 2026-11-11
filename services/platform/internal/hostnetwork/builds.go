package hostnetwork

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// BuildBridge is the host interface of the agent's build network. Every build container is
// attached to it and to nothing else.
const BuildBridge = "astilba-build0"

const (
	buildTable  = "astilba_builds"
	buildMarker = "astilba-builds-v1"
)

// BuildPolicy is what builds on this host may reach. Builds reach the public IPv4 internet
// and, on port 53 only, Resolvers. They never reach the host itself, IPv6, Denied or
// pipeline.NeverAllowed. Of pipeline.AllowableRanges they reach only Allowed.
type BuildPolicy struct {
	// Subnet is the build network's own range, which the policy translates to the host's
	// address. Without the policy builds have no address translation, so no internet.
	Subnet    netip.Prefix
	Resolvers []netip.Addr
	// Denied are private origins refused ahead of any allowance, such as management's.
	Denied  []netip.AddrPort
	Allowed []Destination
}

// Destination is a TCP port on an IPv4 range.
type Destination struct {
	Prefix netip.Prefix
	Port   uint16
}

// BuildRules renders the build network's policy as one nft table.
//
// Rules match the connection's original destination as well as the current one: a
// connection to a port Docker publishes on the host is translated to a container address
// before the forward hook, and must be judged as both. The tables of Docker, ufw and other
// owners still apply, so this table only ever narrows what a build reaches.
func BuildRules(p BuildPolicy) (string, error) {
	if !p.Subnet.IsValid() || !p.Subnet.Addr().Is4() || p.Subnet != p.Subnet.Masked() || !slices.ContainsFunc(pipeline.AllowableRanges, func(r netip.Prefix) bool { return r.Contains(p.Subnet.Addr()) }) {
		return "", errors.New("invalid build network range")
	}
	if len(p.Resolvers) > 3 || len(p.Denied) > 16 || len(p.Allowed) > pipeline.MaxEgressRules+4 {
		return "", errors.New("build network policy exceeds its bounds")
	}
	addresses := func(list []netip.Addr) (string, error) {
		out := []string{}
		for _, a := range list {
			if !a.Is4() {
				return "", errors.New("build network policy takes IPv4 addresses")
			}
			out = append(out, a.String())
		}
		return strings.Join(out, ", "), nil
	}
	resolvers, err := addresses(p.Resolvers)
	if err != nil {
		return "", err
	}
	denied := []string{}
	for _, r := range pipeline.NeverAllowed {
		denied = append(denied, r.String())
	}
	origins := []string{}
	for _, o := range p.Denied {
		if !o.Addr().Is4() || o.Port() == 0 {
			return "", errors.New("invalid denied build destination")
		}
		if !slices.Contains(origins, fmt.Sprintf("%s . %d", o.Addr(), o.Port())) {
			origins = append(origins, fmt.Sprintf("%s . %d", o.Addr(), o.Port()))
		}
	}
	private := []string{}
	for _, r := range pipeline.AllowableRanges {
		private = append(private, r.String())
	}
	allowed := []string{}
	for i, d := range p.Allowed {
		if !d.Prefix.Addr().Is4() || d.Prefix != d.Prefix.Masked() || d.Port == 0 {
			return "", errors.New("invalid allowed build destination")
		}
		// An interval set refuses overlapping elements, and ranges overlap only by nesting.
		covered := slices.ContainsFunc(p.Allowed, func(o Destination) bool {
			return o.Port == d.Port && o.Prefix.Bits() <= d.Prefix.Bits() && o.Prefix.Contains(d.Prefix.Addr()) && (o.Prefix != d.Prefix || slices.Index(p.Allowed, o) < i)
		})
		if !covered {
			allowed = append(allowed, fmt.Sprintf("%s . %d", d.Prefix, d.Port))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\ncomment %q\ncounter refused {}\n", buildTable, buildMarker)
	fmt.Fprintf(&b, "set denied { type ipv4_addr; flags interval; auto-merge; elements = { %s } }\n", strings.Join(denied, ", "))
	fmt.Fprintf(&b, "set private { type ipv4_addr; flags interval; elements = { %s } }\n", strings.Join(private, ", "))
	b.WriteString("set resolvers { type ipv4_addr; ")
	if resolvers != "" {
		fmt.Fprintf(&b, "elements = { %s } ", resolvers)
	}
	b.WriteString("}\nset origins { type ipv4_addr . inet_service; ")
	if len(origins) > 0 {
		fmt.Fprintf(&b, "elements = { %s } ", strings.Join(origins, ", "))
	}
	b.WriteString("}\nset allowed { type ipv4_addr . inet_service; flags interval; ")
	if len(allowed) > 0 {
		fmt.Fprintf(&b, "elements = { %s } ", strings.Join(allowed, ", "))
	}
	b.WriteString("}\n")
	const refuse = "counter name refused reject with icmpx admin-prohibited"
	// Nothing on the host answers a build, apart from a resolver or an allowed destination.
	fmt.Fprintf(&b, `chain input {
type filter hook input priority filter - 5; policy accept;
iifname != %[1]q accept
ct state established,related accept
meta nfproto != ipv4 %[2]s
ip daddr @resolvers meta l4proto { tcp, udp } th dport 53 accept
ip daddr @denied %[2]s
meta l4proto tcp ip daddr . tcp dport @origins %[2]s
meta l4proto tcp ip daddr . tcp dport @allowed accept
%[2]s
}
`, BuildBridge, refuse)
	fmt.Fprintf(&b, `chain forward {
type filter hook forward priority filter - 5; policy accept;
oifname %[1]q ct state established,related accept
oifname %[1]q drop
iifname != %[1]q accept
ct state established,related accept
meta nfproto != ipv4 %[2]s
ip daddr @resolvers meta l4proto { tcp, udp } th dport 53 accept
ct original ip daddr @denied %[2]s
ip daddr @denied %[2]s
meta l4proto tcp ct original ip daddr . ct original proto-dst @origins %[2]s
meta l4proto tcp ct original ip daddr . ct original proto-dst @allowed accept
ct original ip daddr @private %[2]s
ip daddr @private %[2]s
}
`, BuildBridge, refuse)
	fmt.Fprintf(&b, "chain translate {\ntype nat hook postrouting priority srcnat + 5; policy accept;\nip saddr %s oifname != %q masquerade\n}\n}\n", p.Subnet, BuildBridge)
	return b.String(), nil
}

// ApplyBuildPolicy replaces the build network's policy in one transaction. Builds start only
// after it succeeds, so a host restarted without the table runs no build until it is back.
func (h Host) ApplyBuildPolicy(ctx context.Context, p BuildPolicy) error {
	rules, err := BuildRules(p)
	if err != nil {
		return err
	}
	if err = h.publishTable(ctx, buildTable, buildMarker, rules); err != nil {
		return err
	}
	owned, err := h.ownedTable(ctx, buildTable, buildMarker)
	if err == nil && !owned {
		err = errors.New("the build network policy is not installed")
	}
	return err
}

// BuildRefusals counts the packets the policy has refused since it was last applied.
func (h Host) BuildRefusals(ctx context.Context) (uint64, error) {
	raw, err := h.run(ctx, "nft", nil, "-j", "list", "counter", "inet", buildTable, "refused")
	if err != nil {
		return 0, err
	}
	var state struct {
		Items []struct {
			Counter *struct {
				Name    string `json:"name"`
				Packets uint64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if readToolJSON(raw, &state) != nil {
		return 0, errors.New("cannot read the build network policy")
	}
	for _, item := range state.Items {
		if item.Counter != nil && item.Counter.Name == "refused" {
			return item.Counter.Packets, nil
		}
	}
	return 0, errors.New("cannot read the build network policy")
}

// buildResolvers picks the nameservers builds use from resolv.conf contents, preferring
// systemd-resolved's upstream list, since builds can't reach a stub on the host's loopback.
func buildResolvers(upstream, stub []byte) ([]netip.Addr, error) {
	for _, raw := range [][]byte{upstream, stub} {
		if raw == nil {
			continue
		}
		servers, err := parseNameservers(raw)
		if err != nil {
			return nil, err
		}
		out := []netip.Addr{}
		for _, s := range servers {
			if len(out) < 3 && !slices.Contains(out, s.Addr()) {
				out = append(out, s.Addr())
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return []netip.Addr{}, nil
}
