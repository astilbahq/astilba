package agent

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Terminal sessions and scheduled jobs never run in an elevated service: one that is privileged
// or has added capabilities, or that shares a named volume or a network with such a service,
// because a shell or command there can drive the privileged service and so reach the host.
// Management refuses them first; the agent decides again from its journaled plan, from the plan
// a deployment is about to run, and from what the daemon actually runs.

// hostAuthority reports whether a container's own settings give it authority over the host:
// privileged mode, added capabilities, devices, or the host's network, process or IPC
// namespace.
func hostAuthority(h *container.HostConfig) bool {
	return h == nil || h.Privileged || len(h.CapAdd) > 0 || len(h.Devices) > 0 || len(h.DeviceCgroupRules) > 0 || len(h.DeviceRequests) > 0 || h.NetworkMode.IsHost() || h.PidMode.IsHost() || h.IpcMode.IsHost()
}

// elevatedService reports whether a plan treats a service as elevated, see
// execution.ElevatedServiceIDs. A plan that cannot be read is treated as elevating everything.
func elevatedService(p deployment.Plan, app uuid.UUID) bool {
	elevated, err := execution.ElevatedServiceIDs(p.Specification)
	return err != nil || elevated[app]
}

// hostPeer reports whether a container other than v that shares one of its networks or named
// volumes has authority over the host, running or not: a stopped one can be started again.
func (e *Engine) hostPeer(ctx context.Context, v container.InspectResponse) (bool, error) {
	if v.HostConfig == nil || v.HostConfig.NetworkMode.IsContainer() {
		return true, nil
	}
	filters := []client.Filters{}
	if v.NetworkSettings != nil {
		for name := range v.NetworkSettings.Networks {
			filters = append(filters, make(client.Filters).Add("network", name))
		}
	}
	for _, m := range v.Mounts {
		if m.Type == mount.TypeVolume {
			filters = append(filters, make(client.Filters).Add("volume", m.Name))
		}
	}
	peers := map[string]bool{}
	for _, filter := range filters {
		list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
		if err != nil {
			return false, err
		}
		for _, item := range list.Items {
			if item.ID != v.ID {
				peers[item.ID] = true
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(peers)) {
		peer, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if hostAuthority(peer.Container.HostConfig) {
			return true, nil
		}
	}
	return false, nil
}

// terminalFenceWait bounds how long a deployment waits for the shells it ends. Each one's
// cleanup has thirty seconds of its own.
const terminalFenceWait = 45 * time.Second

// terminalFence keeps shells out of services a deployment is about to make elevated. Every
// Compose run passes it first, so a shell opened before a privileged service was deployed
// beside it ends before that service can start.
type terminalFence struct {
	mu sync.Mutex
	// incoming is the last plan each unit's Compose project was run with. A deployment can
	// fail part way, leaving its services running, so that plan's elevated services stay
	// refused until another replaces it. A restarted agent has no shells, and its journaled
	// plan and the daemon decide.
	incoming map[uuid.UUID]incomingPlan
	shells   map[*fencedShell]bool
}

type incomingPlan struct {
	hash     string
	elevated map[uuid.UUID]bool
	// unreadable refuses every service when the plan's elevated services cannot be found.
	unreadable bool
}

func (p incomingPlan) refuses(app uuid.UUID) bool { return p.unreadable || p.elevated[app] }

// fencedShell is a running shell. end asks it to stop; ended closes once its processes have
// been ended.
type fencedShell struct {
	unit, app uuid.UUID
	end       chan struct{}
	ended     chan struct{}
	stopping  bool
}

// fenceTerminals records the plan a unit's Compose project is about to run with, and ends the
// shells in services it makes elevated, waiting until their processes have ended.
func (e *Engine) fenceTerminals(ctx context.Context, p deployment.Plan) error {
	f := &e.terminals
	f.mu.Lock()
	if f.incoming == nil {
		f.incoming = map[uuid.UUID]incomingPlan{}
	}
	next, known := f.incoming[p.UnitID]
	if !known || next.hash != p.Hash || p.Hash == "" {
		elevated, err := execution.ElevatedServiceIDs(p.Specification)
		next = incomingPlan{hash: p.Hash, elevated: elevated, unreadable: err != nil}
		f.incoming[p.UnitID] = next
	}
	ending := []*fencedShell{}
	for shell := range f.shells {
		if shell.unit == p.UnitID && next.refuses(shell.app) {
			if !shell.stopping {
				shell.stopping = true
				close(shell.end)
			}
			ending = append(ending, shell)
		}
	}
	f.mu.Unlock()
	if len(ending) == 0 {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, terminalFenceWait)
	defer cancel()
	for _, shell := range ending {
		select {
		case <-shell.ended:
		case <-wait.Done():
			return errors.New("a terminal session in a service this deployment makes privileged, or makes share a volume or network with one, did not end")
		}
	}
	return nil
}

// incomingRefuses reports whether the plan a unit's Compose project last ran with refuses
// shells in a service.
func (e *Engine) incomingRefuses(unit, app uuid.UUID) bool {
	f := &e.terminals
	f.mu.Lock()
	defer f.mu.Unlock()
	next, known := f.incoming[unit]
	return known && next.refuses(app)
}

// admitShell registers a shell about to start, unless a deployment has since made its service
// elevated. The shell must stop when end closes, and call release once its processes have
// ended.
func (e *Engine) admitShell(unit, app uuid.UUID) (end <-chan struct{}, release func(), ok bool) {
	f := &e.terminals
	f.mu.Lock()
	defer f.mu.Unlock()
	if next, known := f.incoming[unit]; known && next.refuses(app) {
		return nil, nil, false
	}
	if f.shells == nil {
		f.shells = map[*fencedShell]bool{}
	}
	shell := &fencedShell{unit: unit, app: app, end: make(chan struct{}), ended: make(chan struct{})}
	f.shells[shell] = true
	return shell.end, func() {
		f.mu.Lock()
		delete(f.shells, shell)
		f.mu.Unlock()
		close(shell.ended)
	}, true
}

// jobNetworks are the networks a scheduled job joins: exactly its source container's, each of
// which must be one of the plan's own. No aliases are copied, so the job never answers for the
// service.
func jobNetworks(p deployment.Plan, source container.InspectResponse) ([]string, error) {
	owned := map[string]bool{deployment.NetworkName(p.UnitID): true}
	for _, network := range p.Specification.Networks {
		owned[deployment.NetworkName(network.ID)] = true
	}
	networks := []string{}
	if source.NetworkSettings != nil {
		networks = slices.Sorted(maps.Keys(source.NetworkSettings.Networks))
	}
	for _, name := range networks {
		if !owned[name] {
			return nil, errors.New("source network ownership changed")
		}
	}
	return networks, nil
}
