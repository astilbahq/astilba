package agent

import (
	"context"
	"errors"
	"net/netip"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// DefaultNetworkPool is where the agent places the networks it creates. Docker's built-in
// pools hand out /16s and /20s from 172.17.0.0/12 and 192.168.0.0/16, which run out after
// about 30 networks on a stock daemon, and Astilba does not own daemon.json on every host.
// This range is outside those pools, so the owner's own networks keep them, and outside the
// dashboard's suggested managed network prefix, 10.251.0.0/24. It holds 1,024 networks:
// 50 routed environments with 20 networks each, plus ingress.
var DefaultNetworkPool = netip.MustParsePrefix("10.252.0.0/14")

// A unit holds at most 50 containers, plus the proxy and short-lived probes and jobs.
const networkBits = 24

// ErrNetworkPoolFull means the host has no free range for a network the plan needs. The
// agent creates none of the plan's networks then.
var ErrNetworkPoolFull = errors.New("This host has no free address range for another deployment network. Remove an environment from this host or choose another server.")

// errHostRangesUnknown means the agent could not read what the host already routes, so it
// created no network rather than risk one over the owner's LAN or a managed network.
var errHostRangesUnknown = errors.New("The host's routes and managed networks could not be inspected, so no network was created.")

type networkRequest struct {
	name    string
	options client.NetworkCreateOptions
}

// createNetworks creates bridge networks, each with an explicit /24 from the pool that
// overlaps no Docker network, host route, nameserver or managed network prefix. Docker
// avoids only directly connected routes itself, and accepts an explicit subnet over the
// host's own LAN, so the agent chooses. Docker's IPAM remains the record: it refuses a
// subnet overlapping any of its networks, so a creator outside the agent cannot collide.
// Subnets for every request are found before any network is created.
func (e *Engine) createNetworks(ctx context.Context, requests []networkRequest) error {
	if len(requests) == 0 {
		return nil
	}
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	pool := e.networkPool
	if !pool.IsValid() {
		pool = DefaultNetworkPool
	}
	// A second attempt covers a network created outside the agent between reading
	// Docker's networks and creating ours.
	for attempt := 0; ; attempt++ {
		occupied, err := e.occupiedRanges(ctx)
		if err != nil {
			return err
		}
		subnets, ok := freeSubnets(pool, networkBits, occupied, len(requests))
		if !ok {
			return ErrNetworkPoolFull
		}
		for i, r := range requests {
			options := r.options
			options.IPAM = &network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: subnets[i]}}}
			if _, err = e.Docker.NetworkCreate(ctx, r.name, options); err != nil {
				break
			}
		}
		if err == nil {
			return nil
		}
		missing := []networkRequest{}
		for _, r := range requests {
			if _, inspectErr := e.Docker.NetworkInspect(ctx, r.name, client.NetworkInspectOptions{}); errdefs.IsNotFound(inspectErr) {
				missing = append(missing, r)
			}
		}
		// Networks another creator made under these names are judged by their ownership.
		if len(missing) == 0 {
			return nil
		}
		if attempt > 0 {
			return err
		}
		requests = missing
	}
}

// ensureUnitNetworks creates the unit's missing networks together and checks every one is
// the unit's own.
func (e *Engine) ensureUnitNetworks(ctx context.Context, p deployment.Plan) error {
	type desiredNetwork struct {
		name     string
		id       uuid.UUID
		internal bool
	}
	networks := []desiredNetwork{{deployment.NetworkName(p.UnitID), p.UnitID, false}}
	for _, network := range p.Specification.Networks {
		if network.Name == "default" {
			networks[0].internal = network.Internal
			continue
		}
		networks = append(networks, desiredNetwork{deployment.NetworkName(network.ID), network.ID, network.Internal})
	}
	// An existing network keeps the subnet it has, wherever it came from: moving it would
	// disconnect running containers.
	missing := []networkRequest{}
	for _, network := range networks {
		_, err := e.Docker.NetworkInspect(ctx, network.name, client.NetworkInspectOptions{})
		if errdefs.IsNotFound(err) {
			missing = append(missing, networkRequest{network.name, client.NetworkCreateOptions{Driver: "bridge", Internal: network.internal, Labels: visualLabels(p, network.id)}})
		} else if err != nil {
			return errors.New("Deployment network ownership or settings do not match.")
		}
	}
	if err := e.createNetworks(ctx, missing); err != nil {
		if errors.Is(err, ErrNetworkPoolFull) || errors.Is(err, errHostRangesUnknown) {
			return err
		}
		return errors.New("Cannot create the deployment network.")
	}
	for _, network := range networks {
		n, err := e.Docker.NetworkInspect(ctx, network.name, client.NetworkInspectOptions{})
		if err != nil || !visualOwned(n.Network.Labels, p) || n.Network.Driver != "bridge" || n.Network.Internal != network.internal {
			return errors.New("Deployment network ownership or settings do not match.")
		}
	}
	return nil
}

// storageState is a refusal for a full host, which changed nothing, and otherwise asks for
// inspection before conflicting work.
func storageState(err error) api.ExecutionState {
	if errors.Is(err, ErrNetworkPoolFull) {
		return "failed"
	}
	return "needs_attention"
}

// occupiedRanges lists what a new network must avoid: every IPv4 subnet of every Docker
// network on the host, Astilba's or not, and the host's own ranges.
func (e *Engine) occupiedRanges(ctx context.Context) ([]netip.Prefix, error) {
	occupied, err := e.networkHost().Occupied(ctx)
	if err != nil {
		return nil, errHostRangesUnknown
	}
	list, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range list.Items {
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
				occupied = append(occupied, c.Subnet.Masked())
			}
		}
	}
	return occupied, nil
}

// freeSubnets returns the lowest count subnets of the given size inside pool that overlap
// nothing occupied, the order Docker itself allocates in.
func freeSubnets(pool netip.Prefix, bits int, occupied []netip.Prefix, count int) ([]netip.Prefix, bool) {
	pool = pool.Masked()
	out := []netip.Prefix{}
	if bits < pool.Bits() || !pool.Addr().Is4() {
		return nil, false
	}
	step := uint32(1) << (32 - bits)
	first := ipv4(pool.Addr())
	total := uint64(1) << (bits - pool.Bits())
	for i := uint64(0); i < total && len(out) < count; i++ {
		candidate := netip.PrefixFrom(fromIPv4(first+uint32(i)*step), bits)
		free := true
		for _, o := range occupied {
			if o.Overlaps(candidate) {
				free = false
				break
			}
		}
		if free {
			out = append(out, candidate)
		}
	}
	return out, len(out) == count
}

func ipv4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func fromIPv4(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
