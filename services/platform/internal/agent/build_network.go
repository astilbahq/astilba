package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// buildNetworkName is the Docker network every build container joins. Docker translates no
// addresses for it: the agent's build network policy does, after its egress rules.
const buildNetworkName = "astilba-build-network"

// buildNetworkOptions are the bridge settings the build network policy relies on: a known
// interface, no Docker address translation and no traffic between build containers.
var buildNetworkOptions = map[string]string{
	"com.docker.network.bridge.name":                 hostnetwork.BuildBridge,
	"com.docker.network.bridge.enable_ip_masquerade": "false",
	"com.docker.network.bridge.enable_icc":           "false",
}

// buildNetwork creates the owned build network when it is missing and returns its range,
// refusing a network of that name whose settings or owner differ. It holds no workspace's
// data, so it stays the agent's across re-enrollment.
func (e *Engine) buildNetwork(ctx context.Context) (netip.Prefix, error) {
	labels := map[string]string{labelKind: "build-network-v1"}
	_, err := e.Docker.NetworkInspect(ctx, buildNetworkName, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(err) {
		ipv6 := false
		err = e.createNetworks(ctx, []networkRequest{{buildNetworkName, client.NetworkCreateOptions{Driver: "bridge", EnableIPv6: &ipv6, Options: buildNetworkOptions, Labels: labels}}})
		if errors.Is(err, errNetworkPoolFull) || errors.Is(err, errHostRangesUnknown) {
			return netip.Prefix{}, err
		}
	}
	if err != nil {
		return netip.Prefix{}, errors.New("cannot create the build network")
	}
	n, err := e.Docker.NetworkInspect(ctx, buildNetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return netip.Prefix{}, errors.New("cannot inspect the build network")
	}
	v := n.Network
	options := map[string]string{}
	for k := range buildNetworkOptions {
		if value, ok := v.Options[k]; ok {
			options[k] = value
		}
	}
	if !maps.Equal(v.Labels, labels) || v.Driver != "bridge" || v.Internal || v.EnableIPv6 || !maps.Equal(options, buildNetworkOptions) || len(v.IPAM.Config) != 1 {
		return netip.Prefix{}, errors.New("the build network's owner or settings differ; remove " + buildNetworkName + " after inspecting it")
	}
	return v.IPAM.Config[0].Subnet, nil
}

// buildPolicy collects what one build may reach: the runner's allowlist and, when they are
// private, the frozen Git provider and registry this build uses. Management's origin, when
// private, is refused ahead of every allowance.
func (e *Engine) buildPolicy(ctx context.Context, p pipeline.Command, subnet netip.Prefix) (hostnetwork.BuildPolicy, error) {
	policy := hostnetwork.BuildPolicy{Subnet: subnet}
	// The ranges of this host's container bridges, by address rather than interface name,
	// which a host's own bridges can share. Macvlan and ipvlan networks are the LAN itself.
	networks, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return policy, errors.New("cannot list the host's container networks")
	}
	for _, n := range networks.Items {
		for _, c := range n.IPAM.Config {
			if n.Driver == "bridge" && c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
				policy.Contained = append(policy.Contained, c.Subnet.Masked())
			}
		}
	}
	resolvers, err := hostnetwork.BuildResolvers()
	if err != nil {
		return policy, errors.New("cannot read the host's nameservers for builds")
	}
	policy.Resolvers = resolvers
	cfg, err := e.Journal.Config()
	if err != nil {
		return policy, err
	}
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	management, err := url.Parse(cfg.Origin)
	if err != nil {
		return policy, errors.New("invalid management origin")
	}
	// Builds never reach management through the private network, whatever the runner allows.
	resolved, err := resolveOrigin(lookup, management.Host, management.Scheme)
	if err != nil {
		return policy, errors.New("cannot resolve management's address, which builds must not reach")
	}
	if policy.Denied = private(resolved); len(policy.Denied) > hostnetwork.MaxDenied {
		return policy, fmt.Errorf("management resolves to more than %d private addresses, too many to keep builds from", hostnetwork.MaxDenied)
	}
	for _, r := range p.BuildEgress {
		prefix, _ := netip.ParsePrefix(r.Prefix) // validated with the command
		policy.Allowed = append(policy.Allowed, hostnetwork.Destination{Prefix: prefix, Port: uint16(r.Port)})
	}
	type origin struct{ host, scheme string }
	origins := []origin{}
	if u, err := url.Parse(p.Connection.BaseURL); err == nil && u.Hostname() != "" {
		origins = append(origins, origin{u.Host, u.Scheme})
	}
	if p.Registry != nil {
		if host := strings.Split(p.Registry.Repository, "/")[0]; host != "docker.io" {
			origins = append(origins, origin{host, "https"})
		}
	}
	local, err := localAddresses()
	if err != nil {
		return policy, errors.New("cannot read this host's addresses")
	}
	for _, o := range origins {
		resolved, err := resolveOrigin(lookup, o.host, o.scheme)
		if err != nil {
			return policy, fmt.Errorf("cannot resolve %s for the build network", o.host)
		}
		// Builds never reach their own runner, so they can't use a Git provider or registry
		// it serves. Saying so now beats a refused clone or push after the build.
		if slices.ContainsFunc(resolved, func(a netip.AddrPort) bool { return slices.Contains(local, a.Addr()) }) {
			return policy, fmt.Errorf("%s is on this server, which builds can't reach; use a Git provider or registry on another host", o.host)
		}
		for i, a := range private(resolved) {
			// Builds never reach management, so they can't use a service at its address and port.
			if slices.Contains(policy.Denied, a) {
				return policy, fmt.Errorf("%s shares management's private address and port, which builds can't reach; serve it from another address or port", o.host)
			}
			if i < hostnetwork.MaxOriginAddresses {
				policy.Allowed = append(policy.Allowed, hostnetwork.Destination{Prefix: netip.PrefixFrom(a.Addr(), 32), Port: a.Port()})
			}
		}
	}
	return policy, nil
}

// resolveOrigin resolves an origin's host, a name or address with an optional port, to the
// IPv4 addresses and port a connection to it uses. A name that doesn't exist resolves to none.
func resolveOrigin(ctx context.Context, host, scheme string) ([]netip.AddrPort, error) {
	name, rawPort, err := net.SplitHostPort(host)
	if err != nil {
		name, rawPort = strings.Trim(host, "[]"), "443"
		if scheme == "http" {
			rawPort = "80"
		}
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid origin port")
	}
	out := []netip.AddrPort{}
	if name == "" {
		return out, nil
	}
	addresses := []netip.Addr{}
	if a, err := netip.ParseAddr(name); err == nil {
		addresses = append(addresses, a)
	} else {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip4", name)
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
	for _, a := range addresses {
		if a = a.Unmap(); a.Is4() && !slices.Contains(out, netip.AddrPortFrom(a, uint16(port))) {
			out = append(out, netip.AddrPortFrom(a, uint16(port)))
		}
	}
	return out, nil
}

// private keeps the origins an allowance could open, those in pipeline.AllowableRanges:
// builds reach public ones anyway, and never the rest.
func private(origins []netip.AddrPort) []netip.AddrPort {
	return slices.DeleteFunc(slices.Clone(origins), func(o netip.AddrPort) bool {
		a := o.Addr()
		return slices.ContainsFunc(pipeline.NeverAllowed, func(p netip.Prefix) bool { return p.Contains(a) }) || !slices.ContainsFunc(pipeline.AllowableRanges, func(p netip.Prefix) bool { return p.Contains(a) })
	})
}

// localAddresses lists this host's IPv4 addresses.
func localAddresses() ([]netip.Addr, error) {
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := []netip.Addr{}
	for _, i := range interfaces {
		if prefix, err := netip.ParsePrefix(i.String()); err == nil && prefix.Addr().Unmap().Is4() {
			out = append(out, prefix.Addr().Unmap())
		}
	}
	return out, nil
}

// prepareBuildNetwork installs this build's network policy and returns the nameservers
// BuildKit gives RUN steps. It runs before every build container is created.
func (e *Engine) prepareBuildNetwork(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) ([]netip.Addr, error) {
	host := e.networkHost()
	if !host.BuildPolicySupported(ctx) {
		return nil, errors.New(buildPolicyUnsupported)
	}
	subnet, err := e.buildNetwork(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := e.buildPolicy(ctx, p, subnet)
	if err != nil {
		return nil, err
	}
	if err = host.ApplyBuildPolicy(ctx, policy); err != nil {
		return nil, errors.New("cannot install the build network policy")
	}
	return policy.Resolvers, nil
}

// buildPolicyUnsupported names what a host lacks to build, and where the fix is described.
const buildPolicyUnsupported = "Builds need nftables " + hostnetwork.NFTVersion + " and netlink access for the agent; see the build network host requirements in the Git builds documentation."

// buildRefusalNote says how often the policy refused this build, so an owner can tell a
// blocked private mirror from a failing one.
func (e *Engine) buildRefusalNote(ctx context.Context) string {
	n, err := e.networkHost().BuildRefusals(ctx)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf("\nThe build network refused %d packets to private, host or cloud metadata addresses. To use a private mirror off this server, allow it in the runner's build network settings.\n", n)
}
