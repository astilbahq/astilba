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

// terminalNoticeWait is how long a deployment gives the shells it ends to tell their person
// why. Their containers are removed afterwards whether or not they have finished.
const terminalNoticeWait = 10 * time.Second

// terminalFence keeps shells, and anything they started, out of services a deployment is about
// to make elevated. Every Compose run passes it first, so a shell opened before a privileged
// service was deployed beside it ends before that service can start. Ending a shell ends only
// its Linux session, and a person can start a process outside it, so every container a session
// has entered is also removed: Compose then creates it again from its image.
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

// fencedShell is a running shell. end asks it to stop; ended closes once it has.
type fencedShell struct {
	unit, app uuid.UUID
	end       chan struct{}
	ended     chan struct{}
	stopping  bool
}

// enteredContainer is a container a terminal session has entered, journaled until the
// container is gone, so a restarted agent still removes it when its service becomes elevated.
type enteredContainer struct {
	Unit, App uuid.UUID
}

func (j *Journal) enteredContainers(unit uuid.UUID) (map[string]enteredContainer, error) {
	out := map[string]enteredContainer{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("troubleshooting-entered")).ForEach(func(k, _ []byte) error {
			var entered enteredContainer
			if err := j.get(tx, "troubleshooting-entered", string(k), &entered); err != nil {
				return err
			}
			if entered.Unit == unit {
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

// fenceTerminals records the plan a unit's Compose project is about to run with. It ends the
// shells in services that plan makes elevated, then removes every container a session has
// entered in those services, so nothing a person started there survives beside the privileged
// service. It fails, and so stops the run, if a container cannot be removed.
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
	// Admission journals a shell's container under this lock, so every shell admitted so far
	// is in this list.
	entered, err := e.Journal.enteredContainers(p.UnitID)
	f.mu.Unlock()
	if err != nil {
		return err
	}
	notice, cancel := context.WithTimeout(ctx, terminalNoticeWait)
	defer cancel()
	for _, shell := range ending {
		select {
		case <-shell.ended:
		case <-notice.Done():
		}
	}
	for _, id := range slices.Sorted(maps.Keys(entered)) {
		if err := e.removeEntered(ctx, p, id, entered[id], next.refuses(entered[id].App)); err != nil {
			return err
		}
	}
	return nil
}

// removeEntered removes a container a session entered when its service is refused, and forgets
// it once it is gone.
func (e *Engine) removeEntered(ctx context.Context, p deployment.Plan, id string, entered enteredContainer, refused bool) error {
	v, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return e.Journal.forgetEnteredContainer(id)
	}
	if err != nil || !refused {
		return err
	}
	if v.Container.Config == nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != entered.App.String() {
		return errors.New("a container a terminal session entered has changed ownership, so it was not removed before its service became privileged or began sharing a volume or network with one")
	}
	if _, err = e.Docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return errors.New("a container a terminal session entered could not be removed before its service became privileged or began sharing a volume or network with one")
	}
	slog.Info("removed a container a terminal session entered, because its service is becoming elevated", "container", id, "service", entered.App)
	return e.Journal.forgetEnteredContainer(id)
}

// admitShell registers a shell about to start in a container, unless a deployment has since
// made its service elevated, and journals the container as entered. The shell must stop when
// end closes, and call release once it has.
func (e *Engine) admitShell(unit, app uuid.UUID, container string) (end <-chan struct{}, release func(), err error) {
	f := &e.terminals
	f.mu.Lock()
	defer f.mu.Unlock()
	if next, known := f.incoming[unit]; known && next.refuses(app) {
		return nil, nil, ErrTroubleshootingIneligible
	}
	if err = e.Journal.db.Update(func(tx *bolt.Tx) error {
		return e.Journal.put(tx, "troubleshooting-entered", container, enteredContainer{Unit: unit, App: app})
	}); err != nil {
		return nil, nil, err
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
	}, nil
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
