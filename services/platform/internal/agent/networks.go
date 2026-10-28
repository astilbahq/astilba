package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func (e *Engine) networkHost() hostnetwork.Host { return hostnetwork.Host{Dir: e.Journal.dir} }

func networkRestriction(c api.ExecutionCommand) bool {
	var n hostnetwork.Command
	return c.Kind == "network" && c.Network != nil && hostnetwork.Decode(*c.Network, &n) == nil && n.Action == "restrict" && n.Validate() == nil
}
func (e *Engine) executeNetwork(ctx context.Context, c api.ExecutionCommand) error {
	var command hostnetwork.Command
	if c.Network == nil || hostnetwork.Decode(*c.Network, &command) != nil || command.Validate() != nil || hostnetwork.Hash(command) != c.PlanHash || command.NetworkID != c.ResourceId {
		return e.finish(c.Id, "failed", "Managed network command identity is invalid.")
	}
	cfg, err := e.Journal.Config()
	if err != nil {
		return err
	}
	if cfg.Role == "build" || cfg.PreviewOnly {
		return e.finish(c.Id, "failed", "This host role cannot join a managed deployment network.")
	}
	marker, err := os.Lstat(filepath.Join(e.Journal.dir, "network-enabled"))
	if err != nil || !marker.Mode().IsRegular() || marker.Mode().Perm()&0077 != 0 {
		return e.finish(c.Id, "failed", "Managed networking requires explicit supported-host preparation.")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Cancelled before changing managed networking.")
	}
	host := e.networkHost()
	if !host.Supported(ctx) {
		return e.finish(c.Id, "failed", "Managed networking tools, kernel support or service permissions are unavailable.")
	}
	identity, err := host.Identity(command.MembershipID)
	if err != nil {
		return e.finish(c.Id, "needs_attention", "Local network identity requires inspection.")
	}
	receipt := hostnetwork.Receipt{Identity: identity, NetworkID: command.NetworkID}
	if command.Manifest != nil {
		m := *command.Manifest
		if m.ServerID != cfg.ServerID {
			return e.finish(c.Id, "failed", "Managed network controller identity changed.")
		}
		if command.Action == "restrict" {
			prior, err := host.Snapshot(m.NetworkID)
			if err != nil {
				return e.finish(c.Id, "needs_attention", "The admitted private-link authority requires inspection.")
			}
			baseline, unit, err := e.Journal.networkRestrictionScope(c, command)
			if err != nil {
				return e.finish(c.Id, "needs_attention", "The frozen private-link removal scope requires inspection.")
			}
			m, err = hostnetwork.ReconcileRestriction(prior.Manifest, baseline, m, unit, cfg.ControllerGeneration)
			if err != nil {
				return e.finish(c.Id, "needs_attention", "Private-link identity changed outside this removal scope.")
			}
			if hostnetwork.Hash(m) != hostnetwork.Hash(*command.Manifest) {
				receipt.Before, receipt.Effective = &prior.Manifest, &m
			}
		} else if m.ControllerGeneration != cfg.ControllerGeneration && !e.recoveredNetwork(c, m) {
			return e.finish(c.Id, "failed", "Managed network controller identity changed.")
		}
		bridges, err := e.networkBridges(ctx, m)
		if err != nil && command.Action == "restrict" {
			if prior, x := host.Snapshot(m.NetworkID); x == nil && hostnetwork.Restricts(prior.Manifest, m) {
				bridges = prior.Bridges
				err = nil
			}
		}
		if err != nil {
			return e.finish(c.Id, "needs_attention", "Owned network bridges require inspection before changing private links.")
		}
		if err = host.Apply(ctx, m, bridges, command.Action == "restrict"); err != nil {
			return e.finish(c.Id, "needs_attention", "Private-link configuration has an uncertain result. Inspect owned network state before reconciliation.")
		}
		receipt.Generation = m.Generation
		receipt.Hash = hostnetwork.Hash(m)
		receipt.Disabled = m.Disabled
	}
	raw, _ := json.Marshal(receipt)
	return e.Journal.Mutate(c.Id, func(r *Record) {
		r.Report.State = "succeeded"
		r.Report.Note = "Managed network configuration acknowledged; deployment separately verifies endpoint reachability."
		message := json.RawMessage(raw)
		r.Report.Network = &message
	})
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
