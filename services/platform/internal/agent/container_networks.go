package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// DefaultNetworkPool is where the agent places the networks it creates unless the owner sets
// another pool. Docker's built-in pools hand out /16s and /20s from 172.17.0.0/12 and
// 192.168.0.0/16, which run out after about 30 networks on a stock daemon, and Astilba does
// not own daemon.json on every host. This range is outside those pools, so the owner's own
// networks keep them, and outside the dashboard's suggested managed network prefix,
// 10.251.0.0/24. It holds 2,048 networks.
var DefaultNetworkPool = netip.MustParsePrefix("10.252.0.0/14")

// A unit holds at most 50 containers, plus the proxy and short-lived probes and jobs, and a
// /25 has 125 addresses for them. 50 routed environments with 21 networks each, the most a
// Compose project declares, and the ingress network need 1,051.
const networkBits = 25

// minimumNetworkPoolBits is the smallest pool an owner may set: 128 networks, room for 50
// routed environments with one network each and the ingress network, twice over.
const minimumNetworkPoolBits = 18

var privateRanges = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")}

// ValidateNetworkPool checks a pool an owner sets: a private IPv4 range of at least /18,
// written as the range itself. It may hold managed network prefixes, as the default pool
// may: subnets are never chosen inside one the host holds or has reserved, and a host won't
// join a managed network over a route it has or over a container network on it.
func ValidateNetworkPool(pool netip.Prefix) error {
	inside := false
	for _, r := range privateRanges {
		inside = inside || pool.IsValid() && pool.Bits() >= r.Bits() && r.Contains(pool.Addr())
	}
	if !inside || pool != pool.Masked() || pool.Bits() > minimumNetworkPoolBits {
		return fmt.Errorf("The network pool must be a private IPv4 range from /8 to /%d, such as 10.252.0.0/14, written as the range itself.", minimumNetworkPoolBits)
	}
	return nil
}

// errNetworkPoolFull marks a refusal because the pool has no free range for a network the
// plan needs. The agent creates none of the plan's networks then.
var errNetworkPoolFull = errors.New("network pool full")

type poolFullError struct{ message string }

func (e poolFullError) Error() string        { return e.message }
func (e poolFullError) Is(target error) bool { return target == errNetworkPoolFull }

// errHostRangesUnknown means the agent could not read what the host already routes, so it
// created no network rather than risk one over the owner's LAN or a managed network.
var errHostRangesUnknown = errors.New("The host's routes and managed networks could not be inspected, so no network was created.")

type networkRequest struct {
	name    string
	options client.NetworkCreateOptions
}

func (e *Engine) pool() netip.Prefix {
	if e.NetworkPool.IsValid() {
		return e.NetworkPool
	}
	return DefaultNetworkPool
}

// createNetworks creates bridge networks, each with an explicit /25 from the pool that
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
	pool := e.pool()
	managed, routes, docker, err := e.occupiedRanges(ctx)
	if err != nil {
		return err
	}
	subnets, ok := freeSubnets(pool, networkBits, slices.Concat(managed, routes, docker), len(requests))
	if !ok {
		return poolFull(pool, managed, routes, docker, len(requests))
	}
	for i, r := range requests {
		options := r.options
		options.IPAM = &network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: subnets[i]}}}
		if _, err = e.Docker.NetworkCreate(ctx, r.name, options); err != nil {
			return err
		}
	}
	return nil
}

// poolFull says whether the host's own ranges cover the whole pool, which only a different
// pool or managed prefix resolves, naming what covers it, or how much of the pool is left
// for a deployment that needs more.
func poolFull(pool netip.Prefix, managed, routes, docker []netip.Prefix, needed int) error {
	if _, ok := freeSubnets(pool, networkBits, slices.Concat(managed, routes), 1); !ok {
		held := []string{}
		for _, m := range managed {
			if m.Overlaps(pool) {
				held = append(held, m.String())
			}
		}
		routed := slices.ContainsFunc(routes, pool.Overlaps)
		cause := "Host routes cover"
		if len(held) > 0 && routed {
			cause = "Host routes and the managed network prefixes " + strings.Join(held, ", ") + " together cover"
		} else if len(held) > 0 {
			cause = "The managed network prefixes " + strings.Join(held, ", ") + " cover"
		}
		return poolFullError{fmt.Sprintf("%s the whole container network pool %s, so no deployment network can be created and nothing changed. Set --network-pool to a private range outside them.", cause, pool)}
	}
	free, _ := freeSubnets(pool, networkBits, slices.Concat(managed, routes, docker), needed)
	used := 0
	for _, d := range docker {
		if d.Overlaps(pool) {
			used++
		}
	}
	return poolFullError{fmt.Sprintf("This deployment needs %d networks, but the container network pool %s has %d free ranges and %d networks on this host use it. Nothing changed. Deploy to another server, or set a larger --network-pool.", needed, pool, len(free), used)}
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
	// A routed plan for one host is that host's ingress. Its shared network is chosen with the
	// unit's, so a full host refuses before the plan changes anything.
	if p.Local == nil && len(p.Specification.Routes) > 0 {
		ingress := ingressNetwork(p.ServerID)
		if _, err := e.Docker.NetworkInspect(ctx, ingress.name, client.NetworkInspectOptions{}); errdefs.IsNotFound(err) {
			missing = append(missing, ingress)
		} else if err != nil {
			return errors.New("Ingress network ownership or settings do not match.")
		}
	}
	if err := e.createNetworks(ctx, missing); err != nil {
		if errors.Is(err, errNetworkPoolFull) || errors.Is(err, errHostRangesUnknown) {
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

func ingressNetwork(server uuid.UUID) networkRequest {
	return networkRequest{"astilba-ingress-network", client.NetworkCreateOptions{Driver: "bridge", Labels: map[string]string{labelServer: server.String(), labelKind: "ingress-v1"}}}
}

// storageState is a refusal for a full host, which changed nothing, and otherwise asks for
// inspection before conflicting work.
func storageState(err error) api.ExecutionState {
	if errors.Is(err, errNetworkPoolFull) {
		return "failed"
	}
	return "needs_attention"
}

// occupiedRanges lists what a new network must avoid: the host's managed prefixes, including
// one it has reserved for a cross-host candidate, and its routes, and every IPv4 subnet of
// every Docker network on the host, Astilba's or not. They are kept apart so a refusal can say
// what fills the pool.
func (e *Engine) occupiedRanges(ctx context.Context) (managed, routes, docker []netip.Prefix, err error) {
	managed, routes, err = e.networkHost().Occupied(ctx)
	if err != nil {
		return nil, nil, nil, errHostRangesUnknown
	}
	// A reserved host joins the plan's managed network in the links phase. Until then the
	// reservation is its only record of that prefix, so any network made meanwhile, such as
	// the candidate's own, must keep out of it.
	reserved, err := e.Journal.crossHost()
	if err != nil {
		return nil, nil, nil, errHostRangesUnknown
	}
	if reserved.ParentID != uuid.Nil && reserved.Unit.Plan.Local != nil {
		prefix, err := hostnetwork.Prefix(reserved.Unit.Plan.Local.Network.Prefix)
		if err != nil {
			return nil, nil, nil, errHostRangesUnknown
		}
		managed = append(managed, prefix)
	}
	list, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, nil, nil, err
	}
	docker = []netip.Prefix{}
	for _, n := range list.Items {
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
				docker = append(docker, c.Subnet.Masked())
			}
		}
	}
	// A bridge's own routes and addresses are its network's, not the host's.
	routes = slices.DeleteFunc(routes, func(r netip.Prefix) bool {
		return slices.ContainsFunc(docker, func(d netip.Prefix) bool { return d.Bits() <= r.Bits() && d.Contains(r.Addr()) })
	})
	return managed, routes, docker, nil
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
