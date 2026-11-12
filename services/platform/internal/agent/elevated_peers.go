package agent

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// Terminal sessions and scheduled jobs never run in an elevated service: one that is privileged
// or has added capabilities, or that shares a named volume or a network with such a service,
// because a shell or command there can drive the privileged service and so reach the host.
// Management refuses them first; the agent decides again from its journaled plan, from the plan
// a deployment is about to run, and from what the daemon actually runs.

// hostAuthority reports whether a container's own settings give it authority over the host:
// privileged mode, a capability beyond Docker's defaults, devices, or the host's network,
// process or IPC namespace. Adding back a default capability after dropping them all, as
// preview databases and backup helpers do, gives none.
func hostAuthority(h *container.HostConfig) bool {
	return h == nil || h.Privileged || slices.ContainsFunc(h.CapAdd, beyondDefaults) || len(h.Devices) > 0 || len(h.DeviceCgroupRules) > 0 || len(h.DeviceRequests) > 0 || h.NetworkMode.IsHost() || h.PidMode.IsHost() || h.IpcMode.IsHost()
}

// dockerDefaultCapabilities are the capabilities Docker gives every container.
var dockerDefaultCapabilities = map[string]bool{"CHOWN": true, "DAC_OVERRIDE": true, "FSETID": true, "FOWNER": true, "MKNOD": true, "NET_RAW": true, "SETGID": true, "SETUID": true, "SETFCAP": true, "SETPCAP": true, "NET_BIND_SERVICE": true, "SYS_CHROOT": true, "KILL": true, "AUDIT_WRITE": true}

// beyondDefaults reports whether an added capability is one Docker does not give by default,
// including ALL.
func beyondDefaults(capability string) bool {
	return !dockerDefaultCapabilities[strings.TrimPrefix(strings.ToUpper(capability), "CAP_")]
}

// elevatedService reports whether a plan treats a service as elevated, see
// execution.ElevationOf. A plan that cannot be read is treated as elevating everything.
func elevatedService(p deployment.Plan, app uuid.UUID) bool {
	elevation, err := execution.ElevationOf(p.Specification)
	return err != nil || elevation.Services[app]
}

// hostPeer reports whether a container other than v that shares one of its networks or named
// volumes has authority over the host, running or not: a stopped one can be started again.
func (e *Engine) hostPeer(ctx context.Context, v container.InspectResponse) (bool, error) {
	if v.HostConfig.NetworkMode.IsContainer() {
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

// terminalNoticeWait is how long a deployment gives the shells it ends to tell their person
// why. Their containers are stopped afterwards whether or not they have finished.
const terminalNoticeWait = 10 * time.Second

// terminalFence keeps shells, and anything they started, away from a privileged service a
// deployment is about to start. Every Compose run passes it first. Ending a shell ends only its
// Linux session, and a person can start a process outside it, so the fence also stops every
// container a session has entered that the incoming plan would leave able to reach a privileged
// service, with its restart policy cleared so nothing starts it again. Compose then replaces it,
// as it replaces every container of a new plan, keeping its anonymous volumes.
type terminalFence struct {
	mu sync.Mutex
	// incoming is the last plan each unit's Compose project was run with. A deployment can
	// fail part way, leaving its services running, so it stays in force until another
	// replaces it. A restarted agent has no shells, and its journaled plan and the daemon
	// decide.
	incoming map[uuid.UUID]incomingPlan
	shells   map[*fencedShell]bool
	// noticeWait replaces terminalNoticeWait in tests.
	noticeWait time.Duration
}

type incomingPlan struct {
	hash      string
	elevation execution.Elevation
	services  map[uuid.UUID]bool
	// unreadable refuses every container when the plan's elevation cannot be found.
	unreadable bool
}

func readIncoming(p deployment.Plan) incomingPlan {
	elevation, err := execution.ElevationOf(p.Specification)
	services := map[uuid.UUID]bool{}
	for _, app := range p.Specification.Applications {
		services[app.ID] = true
	}
	return incomingPlan{hash: p.Hash, elevation: elevation, services: services, unreadable: err != nil}
}

// refuses reports whether a container of a service, joined to the given networks and named
// volumes, could reach a privileged service of this plan: the service is elevated in it, or is
// not in it and so is being retired, or the container is on a network or volume a privileged
// service of this plan uses, whatever its own service now declares.
func (p incomingPlan) refuses(app uuid.UUID, reach []string) bool {
	if p.unreadable || len(p.elevation.Reach) > 0 && !p.services[app] || p.elevation.Services[app] {
		return true
	}
	return slices.ContainsFunc(reach, func(name string) bool { return p.elevation.Reach[name] })
}

// containerReach names the networks and named volumes a container is joined to.
func containerReach(v container.InspectResponse) []string {
	reach := []string{}
	if v.NetworkSettings != nil {
		reach = slices.Collect(maps.Keys(v.NetworkSettings.Networks))
	}
	for _, m := range v.Mounts {
		if m.Type == mount.TypeVolume {
			reach = append(reach, m.Name)
		}
	}
	return reach
}

// fencedShell is a running shell. end asks it to stop; ended closes once it has.
type fencedShell struct {
	unit      uuid.UUID
	container string
	end       chan struct{}
	ended     chan struct{}
	stopping  bool
}

// enteredContainer is a container a terminal session has entered, journaled until the
// container is gone, so a restarted agent still stops it before a privileged service starts
// beside it.
type enteredContainer struct {
	Unit, App uuid.UUID
}

func (j *Journal) enteredContainers(unit *uuid.UUID) (map[string]enteredContainer, error) {
	out := map[string]enteredContainer{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("troubleshooting-entered")).ForEach(func(k, _ []byte) error {
			var entered enteredContainer
			if err := j.get(tx, "troubleshooting-entered", string(k), &entered); err != nil {
				return err
			}
			if unit == nil || entered.Unit == *unit {
				out[string(k)] = entered
			}
			return nil
		})
	})
	return out, err
}

func (j *Journal) forgetEnteredContainer(id string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("troubleshooting-entered")).Delete([]byte(id))
	})
}

// ForgetRemovedContainers runs at startup and forgets entered containers that no longer
// exist, such as those of a retired preview or cross-host unit that no Compose run fenced.
func (e *Engine) ForgetRemovedContainers(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entered, err := e.Journal.enteredContainers(nil)
	for id := range entered {
		if err != nil {
			break
		}
		if _, err = e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); errdefs.IsNotFound(err) {
			err = e.Journal.forgetEnteredContainer(id)
		}
	}
	if err != nil {
		slog.Error("forgetting removed terminal session containers failed", "error", err)
	}
}

// fenceTerminals records the plan a unit's Compose project is about to run with, and returns
// the arguments to run it with. When the run can start a privileged or added-capability
// service, every container a session has entered that this plan refuses has its shells ended
// and is stopped first, so nothing a person started there runs beside that service. It fails,
// and so stops the run, if such a container cannot be stopped. A run that starts a service a
// session entered recreates its container, never starting the old one again, even when the
// plan is the one that container was created with.
func (e *Engine) fenceTerminals(ctx context.Context, p deployment.Plan, args []string) ([]string, error) {
	f := &e.terminals
	f.mu.Lock()
	if f.incoming == nil {
		f.incoming = map[uuid.UUID]incomingPlan{}
	}
	next, known := f.incoming[p.UnitID]
	if !known || next.hash != p.Hash || p.Hash == "" {
		next = readIncoming(p)
		f.incoming[p.UnitID] = next
	}
	// Admission journals a shell's container under this lock, so every shell admitted so far
	// is in this list, and any admitted later decides with this plan.
	entered, err := e.Journal.enteredContainers(&p.UnitID)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	targets := map[uuid.UUID]bool{}
	for _, app := range p.Specification.Applications {
		if slices.Contains(args, deployment.ServiceName(app.ID)) {
			targets[app.ID] = true
		}
	}
	up := slices.Index(args, "up")
	if up >= 0 && !slices.Contains(args, "--force-recreate") && slices.ContainsFunc(slices.Collect(maps.Values(entered)), func(c enteredContainer) bool { return len(targets) == 0 || targets[c.App] }) {
		args = slices.Insert(slices.Clone(args), up+1, "--force-recreate")
	}
	starts := up >= 0 && !slices.Contains(args, "--no-start") && (next.unreadable || len(targets) == 0 || slices.ContainsFunc(slices.Collect(maps.Keys(targets)), func(app uuid.UUID) bool { return next.elevation.Privileged[app] }))
	if !starts {
		return args, nil
	}
	refused := []string{}
	for _, id := range slices.Sorted(maps.Keys(entered)) {
		v, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			if err = e.Journal.forgetEnteredContainer(id); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		// Nothing runs in a dead container, or one the fence has already stopped.
		state, host := v.Container.State, v.Container.HostConfig
		if state != nil && (state.Dead || state.Status == container.StateRemoving || !state.Running && host != nil && host.RestartPolicy.Name == container.RestartPolicyDisabled) {
			continue
		}
		if next.refuses(entered[id].App, containerReach(v.Container)) {
			refused = append(refused, id)
		}
	}
	if len(refused) == 0 {
		return args, nil
	}
	ending := []*fencedShell{}
	f.mu.Lock()
	for shell := range f.shells {
		if shell.unit == p.UnitID && slices.Contains(refused, shell.container) {
			if !shell.stopping {
				shell.stopping = true
				close(shell.end)
			}
			ending = append(ending, shell)
		}
	}
	wait := f.noticeWait
	f.mu.Unlock()
	if wait == 0 {
		wait = terminalNoticeWait
	}
	notice, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for _, shell := range ending {
		select {
		case <-shell.ended:
		case <-notice.Done():
		}
	}
	for _, id := range refused {
		// The container stops as it is configured to, so its service can finish its writes.
		_, err := e.Docker.ContainerUpdate(ctx, id, client.ContainerUpdateOptions{RestartPolicy: &container.RestartPolicy{Name: container.RestartPolicyDisabled}})
		if err == nil {
			_, err = e.Docker.ContainerStop(ctx, id, client.ContainerStopOptions{})
		}
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("a container a terminal session entered could not be stopped before a privileged service starts beside it")
		}
		slog.Info("stopped a container a terminal session entered, because a privileged service can reach it", "container", id, "service", entered[id].App)
	}
	return args, nil
}

// incomingRefuses reports whether the plan a unit's Compose project last ran with refuses a
// container of a service, see incomingPlan.refuses.
func (e *Engine) incomingRefuses(unit, app uuid.UUID, v container.InspectResponse) bool {
	f := &e.terminals
	f.mu.Lock()
	defer f.mu.Unlock()
	next, known := f.incoming[unit]
	return known && next.refuses(app, containerReach(v))
}

// admitShell registers a shell about to start in a container and journals the container as
// entered. Its caller decides eligibility again afterwards, so a fence before admission is seen
// by that check and one after it ends the shell. The shell must stop when end closes, and call
// release once it has.
func (e *Engine) admitShell(unit, app uuid.UUID, container string) (end <-chan struct{}, release func(), err error) {
	f := &e.terminals
	f.mu.Lock()
	defer f.mu.Unlock()
	if err = e.Journal.db.Update(func(tx *bolt.Tx) error {
		return e.Journal.put(tx, "troubleshooting-entered", container, enteredContainer{Unit: unit, App: app})
	}); err != nil {
		return nil, nil, err
	}
	if f.shells == nil {
		f.shells = map[*fencedShell]bool{}
	}
	shell := &fencedShell{unit: unit, container: container, end: make(chan struct{}), ended: make(chan struct{})}
	f.shells[shell] = true
	return shell.end, func() {
		f.mu.Lock()
		delete(f.shells, shell)
		f.mu.Unlock()
		close(shell.ended)
	}, nil
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
