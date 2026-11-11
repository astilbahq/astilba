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
	policy := hostnetwork.BuildPolicy{Subnet: subnet, Contained: []netip.Prefix{e.pool()}}
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
	if policy.Denied, err = privateOrigins(lookup, management.Host, management.Scheme); err != nil {
		return policy, errors.New("cannot resolve management's address, which builds must not reach")
	}
	if len(policy.Denied) > hostnetwork.MaxDenied {
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
	for _, o := range origins {
		allowed, err := privateOrigins(lookup, o.host, o.scheme)
		if err != nil {
			return policy, fmt.Errorf("cannot resolve %s for the build network", o.host)
		}
		for i, a := range allowed {
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

// privateOrigins resolves an origin's host, a name or address with an optional port, to the
// private IPv4 addresses and port a connection to it uses.
func privateOrigins(ctx context.Context, host, scheme string) ([]netip.AddrPort, error) {
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
	addresses, err := privateAddresses(ctx, name)
	out := []netip.AddrPort{}
	for _, a := range addresses {
		out = append(out, netip.AddrPortFrom(a, uint16(port)))
	}
	return out, err
}

// privateAddresses resolves host and keeps the IPv4 addresses an allowance could open,
// those in pipeline.AllowableRanges: builds reach public ones anyway, and never the rest.
func privateAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if host == "" {
		return []netip.Addr{}, nil
	}
	var addresses []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{a}
	} else {
		var err error
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return []netip.Addr{}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	out := []netip.Addr{}
	for _, a := range addresses {
		a = a.Unmap()
		if a.Is4() && !slices.ContainsFunc(pipeline.NeverAllowed, func(p netip.Prefix) bool { return p.Contains(a) }) && slices.ContainsFunc(pipeline.AllowableRanges, func(p netip.Prefix) bool { return p.Contains(a) }) && !slices.Contains(out, a) {
			out = append(out, a)
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
