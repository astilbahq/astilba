package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func (e *Engine) networkHost() hostnetwork.Host { return hostnetwork.Host{Dir: e.Journal.dir} }

// managedHost is the part of the host's managed networking that executeNetwork uses.
type managedHost interface {
	Supported(context.Context) bool
	Identity(uuid.UUID) (hostnetwork.Identity, error)
	Snapshot(uuid.UUID) (hostnetwork.Snapshot, error)
	Apply(context.Context, hostnetwork.Manifest, []hostnetwork.Bridge, bool) error
}

func (e *Engine) managedNetworking() managedHost {
	if e.managed != nil {
		return e.managed
	}
	return e.networkHost()
}

func networkRestriction(c api.ExecutionCommand) bool {
	var n hostnetwork.Command
	return c.Kind == "network" && c.Network != nil && hostnetwork.Decode(*c.Network, &n) == nil && n.Action == "restrict" && n.Validate() == nil
}
func (e *Engine) executeNetwork(ctx context.Context, c api.ExecutionCommand, r Record) error {
	finish := func(state api.ExecutionState, note string) error { return e.finishPhase(ctx, c.Id, state, note) }
	var command hostnetwork.Command
	if c.Network == nil || hostnetwork.Decode(*c.Network, &command) != nil || command.Validate() != nil || hostnetwork.Hash(command) != c.PlanHash || command.NetworkID != c.ResourceId {
		return finish("failed", "Managed network command identity is invalid.")
	}
	cfg, err := e.Journal.Config()
	if err != nil {
		return err
	}
	if cfg.Role == "build" || cfg.PreviewOnly {
		return finish("failed", "This host role cannot join a managed deployment network.")
	}
	marker, err := os.Lstat(filepath.Join(e.Journal.dir, "network-enabled"))
	if err != nil || !marker.Mode().IsRegular() || marker.Mode().Perm()&0077 != 0 {
		return finish("failed", "Managed networking requires explicit supported-host preparation.")
	}
	if e.cancelled(c.Id) && r.Checkpoint == "accepted" {
		return finish("cancelled", "Cancelled before changing managed networking.")
	}
	host := e.managedNetworking()
	if !host.Supported(ctx) {
		return finish("failed", "Managed networking tools, kernel support or service permissions are unavailable.")
	}
	identity, err := host.Identity(command.MembershipID)
	if err != nil {
		return finish("needs_attention", "Local network identity requires inspection.")
	}
	receipt := hostnetwork.Receipt{Identity: identity, NetworkID: command.NetworkID}
	if command.Manifest != nil {
		m := *command.Manifest
		if m.ServerID != cfg.ServerID {
			return finish("failed", "Managed network controller identity changed.")
		}
		if removalOfNothing(host, command) && (command.Action == "restrict" || m.ControllerGeneration == cfg.ControllerGeneration) {
			receipt.Generation, receipt.Hash, receipt.Disabled = m.Generation, hostnetwork.Hash(m), m.Disabled
			return e.acknowledgeNetwork(c.Id, receipt)
		}
		if command.Action == "restrict" {
			prior, err := host.Snapshot(m.NetworkID)
			if err != nil {
				return finish("needs_attention", "The admitted private-link authority requires inspection.")
			}
			baseline, unit, err := e.Journal.networkRestrictionScope(c, command)
			if err != nil {
				return finish("needs_attention", "The frozen private-link removal scope requires inspection.")
			}
			m, err = hostnetwork.ReconcileRestriction(prior.Manifest, baseline, m, unit, cfg.ControllerGeneration)
			if err != nil {
				if c.ParentId == nil {
					return e.refuseNetwork(ctx, c.Id, receipt, "This removal no longer fits this host's private links, so nothing changed.")
				}
				return finish("needs_attention", "Private-link identity changed outside this removal scope.")
			}
			if hostnetwork.Hash(m) != hostnetwork.Hash(*command.Manifest) {
				receipt.Before, receipt.Effective = &prior.Manifest, &m
			}
		} else if m.ControllerGeneration != cfg.ControllerGeneration && !e.recoveredNetwork(c, m) {
			return finish("failed", "Managed network controller identity changed.")
		}
		bridges, err := e.networkBridges(ctx, m)
		if err != nil && command.Action == "restrict" {
			if prior, x := host.Snapshot(m.NetworkID); x == nil && hostnetwork.Restricts(prior.Manifest, m) {
				bridges = prior.Bridges
				err = nil
			}
		}
		if err != nil {
			return finish("needs_attention", "Owned network bridges require inspection before changing private links.")
		}
		// Once begun, an apply finishes even past its admission deadline, as a phase's rerun.
		if err = e.checkpoint(c.Id, "network-apply"); err != nil {
			return err
		}
		var conflict hostnetwork.RouteConflictError
		e.networkMu.Lock()
		err = e.containerNetworkConflict(ctx, host, command)
		inspected := err == nil || errors.As(err, &conflict)
		if err == nil {
			err = host.Apply(ctx, m, bridges, command.Action == "restrict")
		}
		e.networkMu.Unlock()
		if !inspected {
			// Docker's record is unread and nothing changed. The command reruns from its
			// checkpoint, after a Docker restart or the agent's own.
			return err
		}
		if errors.As(err, &conflict) {
			// Management may hold no record of what this host has, after a lost receipt or a
			// refused removal, and then it can't scope a removal. The host says, so revoked
			// peers still leave it while the conflict stands. What it holds under an earlier
			// membership, before it was removed and added again, scopes nothing now.
			if held, err := host.Snapshot(m.NetworkID); err == nil && held.Manifest.MembershipID == m.MembershipID {
				receipt.Before = &held.Manifest
			}
			return e.refuseRouteConflict(ctx, c, receipt, conflict.Range)
		}
		if err != nil {
			if c.ParentId == nil && errors.Is(err, hostnetwork.ErrGenerationConflict) {
				return e.refuseNetwork(ctx, c.Id, receipt, "This host holds a later private-link generation than this controller issued, so nothing changed. Fence this server with prepare-recovery, transfer it, then approve the network again.")
			}
			return finish("needs_attention", "Private-link configuration has an uncertain result. Inspect owned network state before reconciliation.")
		}
		receipt.Generation = m.Generation
		receipt.Hash = hostnetwork.Hash(m)
		receipt.Disabled = m.Disabled
	}
	return e.acknowledgeNetwork(c.Id, receipt)
}

func (e *Engine) acknowledgeNetwork(id uuid.UUID, receipt hostnetwork.Receipt) error {
	raw, _ := stablejson.Marshal(receipt)
	return e.Journal.Mutate(id, func(r *Record) {
		r.Report.State = "succeeded"
		for i := range r.Report.Steps {
			r.Report.Steps[i].State = "done"
		}
		r.Report.Note = "Managed network configuration acknowledged; deployment separately verifies endpoint reachability."
		message := jsontext.Value(raw)
		r.Report.Network = &message
	})
}

// refuseNetwork ends a standalone command the host refused before changing anything. An unfinished
// record would block other commands and controller transfer, the way out of a generation conflict.
// A coordinated parent's phases keep needs_attention, which its own recovery resolves.
func (e *Engine) refuseNetwork(ctx context.Context, id uuid.UUID, receipt hostnetwork.Receipt, note string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt.Refused = true
	raw, _ := stablejson.Marshal(receipt)
	return e.Journal.Mutate(id, func(r *Record) {
		r.Report.State = "failed"
		r.Report.Note = note
		message := jsontext.Value(raw)
		r.Report.Network = &message
	})
}

// refuseRouteConflict ends a configure the host refused because part of the network's prefix
// is in use here. Nothing changed, so a coordinated phase fails and its parent aborts, which
// releases the deployment's reservations. A standalone configure tells management why, so its
// note names the remedy rather than a generation conflict's. Older management can't read that,
// so it gets what older agents report. Until management has answered since the agent started,
// which it may not have when a command reruns, the agent can't tell which, so the command waits.
func (e *Engine) refuseRouteConflict(ctx context.Context, c api.ExecutionCommand, receipt hostnetwork.Receipt, overlap netip.Prefix) error {
	conflict := "A route or container network on this host uses " + overlap.String() + ", which overlaps the managed network prefix, so nothing changed. "
	if c.ParentId != nil {
		return e.finishPhase(ctx, c.Id, "failed", conflict+"Remove that network or route on this host, then deploy again.")
	}
	switch e.networkRefusals.Load() {
	case refusalsUnknown:
		return errors.New("management has not yet said whether it reads network refusal reasons")
	case refusalsUnread:
		return e.finishPhase(ctx, c.Id, "needs_attention", conflict+"Remove that network or route on this host.")
	}
	receipt.Reason = hostnetwork.RouteConflict
	return e.refuseNetwork(ctx, c.Id, receipt, conflict+"Remove that network or route on this host, then save the network again.")
}

// containerNetworkConflict refuses a first join over a container network on this host. The
// host's route check sees a container network only while its bridge has a route, and a bridge
// has none before Docker starts after a reboot, which the agent's unit doesn't wait for, or
// in isolated gateway mode. A network this host already holds enabled, under any membership,
// needs no check: new container networks avoid its prefix, and applying replaces an earlier
// membership's peers. A disabled one doesn't count, since its removal was not checked. Nor does
// a disabled manifest, which is how management removes a
// member it has no record of: refusing it would leave that member's removal pending for good,
// and it installs no peers, only its own address on an interface it leaves down. The route
// check still applies to it. Only networks with addresses on this host count, so swarm-scoped
// and config-only networks, and ipvlan in L3 mode, which the host doesn't route to, are left
// out. An error other than a RouteConflictError means Docker's record was not read.
func (e *Engine) containerNetworkConflict(ctx context.Context, host managedHost, command hostnetwork.Command) error {
	if command.Action != "configure" || command.Manifest.Disabled {
		return nil
	}
	held, err := host.Snapshot(command.NetworkID)
	if err == nil && !held.Manifest.Disabled || err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	list, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return err
	}
	// Validate has checked the prefix.
	prefix, _ := hostnetwork.Prefix(command.Manifest.Prefix)
	for _, n := range list.Items {
		// A network made from a config-only one is listed with the subnets and options it took.
		if n.Scope != "local" || n.ConfigOnly {
			continue
		}
		switch n.Driver {
		case "bridge", "macvlan":
		case "ipvlan":
			if mode := n.Options["ipvlan_mode"]; mode != "" && mode != "l2" {
				continue
			}
		default:
			continue
		}
		for _, ipam := range n.IPAM.Config {
			if ipam.Subnet.Overlaps(prefix) {
				return hostnetwork.RouteConflictError{Range: ipam.Subnet.Masked()}
			}
		}
	}
	return nil
}

// removalOfNothing is a member's removal, its first or a later one, on a host that never joined
// the network. Applying it would install a disabled interface for nothing, and could be refused
// over a route the host uses, or fail to scope a restriction against state the host doesn't
// have, and either would leave the removal pending for good, holding the whole network back. The
// host already holds what the manifest says, no peers and no grants, so it acknowledges it as
// is. Only a disabled manifest qualifies, which Validate keeps empty, so nothing that grants is
// ever acknowledged unapplied. A host keeping an earlier membership's network applies the
// removal, which replaces that membership, as recovery expects of its export.
func removalOfNothing(host managedHost, command hostnetwork.Command) bool {
	if !command.Manifest.Disabled {
		return false
	}
	_, err := host.Snapshot(command.NetworkID)
	return errors.Is(err, os.ErrNotExist)
}

// What management said about refusal reasons, in Engine.networkRefusals.
const (
	refusalsUnknown int32 = iota
	refusalsUnread
	refusalsRead
)

// withoutRefusalReason is a report's receipt as management that reads no reasons accepts it:
// none, if it gives one, so the report is a plain failure.
func withoutRefusalReason(network *jsontext.Value) *jsontext.Value {
	var receipt hostnetwork.Receipt
	if network != nil && hostnetwork.Decode(*network, &receipt) == nil && receipt.Reason != "" {
		return nil
	}
	return network
}

func (e *Engine) networkBridges(ctx context.Context, m hostnetwork.Manifest) ([]hostnetwork.Bridge, error) {
	cfg, err := e.Journal.Config()
	if err != nil {
		return nil, err
	}
	units := map[uuid.UUID]bool{}
	for _, v := range m.Exports {
		units[v.UnitID] = true
	}
	for _, v := range m.Egress {
		units[v.UnitID] = true
	}
	ids := []uuid.UUID{}
	for id := range units {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	out := []hostnetwork.Bridge{}
	for _, id := range ids {
		v, err := e.Docker.NetworkInspect(ctx, deployment.NetworkName(id), client.NetworkInspectOptions{})
		if err != nil || v.Network.Driver != "bridge" || v.Network.Labels[labelServer] != cfg.ServerID.String() || v.Network.Labels["com.astilba.unit"] != id.String() || v.Network.Labels[labelKind] != "visual-v1" {
			return nil, errors.New("network ownership mismatch")
		}
		name := v.Network.Options["com.docker.network.bridge.name"]
		if name == "" {
			if len(v.Network.ID) < 12 {
				return nil, errors.New("network identity missing")
			}
			name = "br-" + v.Network.ID[:12]
		}
		b := hostnetwork.Bridge{UnitID: id, Name: name, Sources: []string{}}
		for _, c := range v.Network.IPAM.Config {
			if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
				b.Sources = append(b.Sources, c.Subnet.String())
			}
		}
		if len(b.Sources) == 0 {
			return nil, errors.New("network source prefix unavailable")
		}
		for _, egress := range m.Egress {
			if egress.UnitID != id || !egress.Ingress {
				continue
			}
			b.Proxy, err = e.ingressNetworkPaths(ctx, cfg.ServerID)
			if err != nil {
				return nil, err
			}
			break
		}
		out = append(out, b)
	}
	return out, nil
}

func (e *Engine) ingressNetworkPaths(ctx context.Context, server uuid.UUID) ([]hostnetwork.ProxyPath, error) {
	proxy, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if proxy.Container.Config.Labels[labelServer] != server.String() || proxy.Container.Config.Labels[labelKind] != "ingress-v1" || !proxy.Container.State.Running || proxy.Container.NetworkSettings == nil {
		return nil, errors.New("owned running ingress is unavailable")
	}
	const name = "astilba-ingress-network"
	attachment := proxy.Container.NetworkSettings.Networks[name]
	if attachment == nil || attachment.GwPriority != 100 {
		return nil, errors.New("review primary host changes to prepare the dedicated ingress gateway first")
	}
	for other, endpoint := range proxy.Container.NetworkSettings.Networks {
		if other != name && endpoint.GwPriority >= 100 {
			return nil, errors.New("ingress default gateway changed outside the approved network")
		}
	}
	network, err := e.Docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		return nil, err
	}
	if network.Network.Driver != "bridge" || network.Network.Labels[labelServer] != server.String() || network.Network.Labels[labelKind] != "ingress-v1" || len(network.Network.ID) < 12 {
		return nil, errors.New("dedicated ingress network ownership mismatch")
	}
	bridge := network.Network.Options["com.docker.network.bridge.name"]
	if bridge == "" {
		bridge = "br-" + network.Network.ID[:12]
	}
	paths := []hostnetwork.ProxyPath{}
	for _, ipam := range network.Network.IPAM.Config {
		if ipam.Subnet.IsValid() && ipam.Subnet.Addr().Is4() {
			paths = append(paths, hostnetwork.ProxyPath{Name: bridge, Prefix: ipam.Subnet.String()})
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("dedicated ingress prefix is unavailable")
	}
	return paths, nil
}
