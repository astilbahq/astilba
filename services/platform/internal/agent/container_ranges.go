package agent

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// dockerDefaultAddressPools are the pools a Docker daemon allocates network subnets from when
// daemon.json sets no default-address-pools: Moby's local-scope defaults.
var dockerDefaultAddressPools = []netip.Prefix{
	netip.MustParsePrefix("172.17.0.0/16"),
	netip.MustParsePrefix("172.18.0.0/16"),
	netip.MustParsePrefix("172.19.0.0/16"),
	netip.MustParsePrefix("172.20.0.0/14"),
	netip.MustParsePrefix("172.24.0.0/14"),
	netip.MustParsePrefix("172.28.0.0/14"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// dockerDerivedULA covers the IPv6 pool a daemon adds when its configured pools hold none: a
// unique local /48 derived from the daemon's host ID, which Docker doesn't report.
var dockerDerivedULA = netip.MustParsePrefix("fd00::/8")

// maxContainerRanges bounds the report so a heartbeat stays well inside its body limit. A host
// with more ranges than this outside its pools reports none, which management reads as unknown.
const maxContainerRanges = 64

var errTooManyContainerRanges = errors.New("too many container ranges")

const containerRangesUnreadNote = "The agent could not read the address ranges this host's containers send traffic from, so a plan for a route staged with a client allowlist needs an owner's review."

// ReportContainerRanges adds to h every range a container on this host can send traffic from,
// so management can refuse a client allowlist that would also admit them. Only the agent's run
// process knows its network pool, so it alone reports them. A range it can't read leaves out
// the whole list, which management reads as unknown rather than as a host without containers.
func (e *Engine) ReportContainerRanges(ctx context.Context, h *api.AgentHeartbeat) {
	supported := true
	h.Capabilities.ContainerRanges = &supported
	ranges, err := e.containerRanges(ctx)
	if err != nil {
		h.Preflight.Notes = append(h.Preflight.Notes, containerRangesUnreadNote)
		return
	}
	h.Preflight.ContainerRanges = &ranges
}

func (e *Engine) containerRanges(ctx context.Context) ([]api.ContainerRange, error) {
	if e.Docker == nil {
		return nil, errors.New("Docker is unavailable")
	}
	info, err := e.Docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, err
	}
	list, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	managed, err := e.networkHost().ManagedPrefixes()
	if err != nil {
		return nil, err
	}
	// A reserved cross-host candidate joins its managed network later; until then the
	// reservation is the only record of that prefix, as for occupiedRanges.
	reserved, err := e.Journal.crossHost()
	if err != nil {
		return nil, err
	}
	if reserved.ParentID != uuid.Nil && reserved.Unit.Plan.Local != nil {
		prefix, err := hostnetwork.Prefix(reserved.Unit.Plan.Local.Network.Prefix)
		if err != nil {
			return nil, err
		}
		managed = append(managed, prefix)
	}
	bridge, networks := []netip.Prefix{}, []netip.Prefix{}
	for _, n := range list.Items {
		for _, c := range n.IPAM.Config {
			if !c.Subnet.IsValid() {
				continue
			}
			if n.Name == "bridge" {
				bridge = append(bridge, c.Subnet.Masked())
			} else {
				networks = append(networks, c.Subnet.Masked())
			}
		}
	}
	return collectContainerRanges(e.pool(), bridge, addressPools(info.Info.DefaultAddressPools), managed, networks)
}

// addressPools are the pools Docker allocates a network's subnet from when none is named.
func addressPools(configured []system.NetworkAddressPool) []netip.Prefix {
	out := []netip.Prefix{}
	for _, p := range configured {
		if p.Base.IsValid() {
			out = append(out, p.Base.Masked())
		}
	}
	// Configured pools replace Docker's IPv4 defaults; an IPv6 one is derived whenever none is set.
	if len(out) == 0 {
		out = slices.Clone(dockerDefaultAddressPools)
	}
	if !slices.ContainsFunc(out, func(p netip.Prefix) bool { return p.Addr().Is6() && !p.Addr().Is4In6() }) {
		out = append(out, dockerDerivedULA)
	}
	return out
}

// collectContainerRanges lists each range once, leaving out any inside one already listed, in
// the order of its arguments so a range is named by the most telling source that holds it.
func collectContainerRanges(pool netip.Prefix, bridge, pools, managed, networks []netip.Prefix) ([]api.ContainerRange, error) {
	type sourced struct {
		prefixes []netip.Prefix
		source   api.ContainerRangeSource
	}
	all := []api.ContainerRange{}
	kept := []netip.Prefix{}
	add := func(p netip.Prefix, source api.ContainerRangeSource) {
		p = p.Masked()
		// Contains never matches across address families.
		if slices.ContainsFunc(kept, func(k netip.Prefix) bool { return k.Bits() <= p.Bits() && k.Contains(p.Addr()) }) {
			return
		}
		// A wider range found later replaces the narrower ones it holds.
		for i := len(kept) - 1; i >= 0; i-- {
			if p.Bits() < kept[i].Bits() && p.Contains(kept[i].Addr()) {
				kept, all = slices.Delete(kept, i, i+1), slices.Delete(all, i, i+1)
			}
		}
		kept = append(kept, p)
		all = append(all, api.ContainerRange{Prefix: p.String(), Source: source})
	}
	networks = slices.Clone(networks)
	slices.SortFunc(networks, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
	for _, s := range []sourced{{[]netip.Prefix{pool}, api.ContainerRangeSourceNetworkPool}, {bridge, api.ContainerRangeSourceDefaultBridge}, {pools, api.ContainerRangeSourceAddressPool}, {managed, api.ContainerRangeSourceManagedNetwork}, {networks, api.ContainerRangeSourceDockerNetwork}} {
		for _, p := range s.prefixes {
			if p.IsValid() {
				add(p, s.source)
			}
		}
	}
	if len(all) > maxContainerRanges {
		return nil, errTooManyContainerRanges
	}
	return all, nil
}
