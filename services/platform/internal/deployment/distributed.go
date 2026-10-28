package deployment

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

const CrossHostRecipe = 4

type ResourceRequest struct {
	CPUMillis int `json:"cpuMillis"`
	MemoryMiB int `json:"memoryMiB"`
}
type PrivateEndpoint struct {
	ServerID      uuid.UUID `json:"serverId"`
	UnitID        uuid.UUID `json:"unitId"`
	ResourceID    uuid.UUID `json:"resourceId"`
	InstanceID    uuid.UUID `json:"instanceId"`
	Address       string    `json:"address"`
	Port          int       `json:"port"`
	ContainerPort int       `json:"containerPort"`
}
type LocalPlacement struct {
	GroupID        uuid.UUID                    `json:"groupId"`
	Primary        bool                         `json:"primary"`
	Network        hostnetwork.Manifest         `json:"network"`
	Instances      map[string][]uuid.UUID       `json:"instances"`
	PriorInstances map[string][]uuid.UUID       `json:"priorInstances"`
	Requests       map[string]ResourceRequest   `json:"requests"`
	Outputs        map[string]PrivateEndpoint   `json:"outputs"`
	Backends       map[string][]PrivateEndpoint `json:"backends"`
}
type DistributedUnit struct {
	Plan          Plan   `json:"plan"`
	Previous      Plan   `json:"previous"`
	CPUMillis     int64  `json:"cpuMillis"`
	MemoryMiB     int64  `json:"memoryMiB"`
	InventoryHash string `json:"inventoryHash"`
}
type DistributedPlan struct {
	NetworkID         uuid.UUID         `json:"networkId"`
	NetworkGeneration int64             `json:"networkGeneration"`
	Units             []DistributedUnit `json:"units"`
}

func (p Plan) ActiveUnits() []DistributedUnit {
	if p.Distributed == nil {
		return nil
	}
	out := []DistributedUnit{}
	for _, unit := range p.Distributed.Units {
		if unit.Plan.Local.Primary || len(unit.Plan.LocalApplications()) > 0 {
			out = append(out, unit)
		}
	}
	return out
}

type CrossHostCommand struct {
	Maintenance    *Maintenance                   `json:"maintenance,omitempty"`
	EffectAction   string                         `json:"effectAction,omitempty"`
	UseTarget      bool                           `json:"useTarget,omitempty"`
	ParentID       uuid.UUID                      `json:"parentId"`
	GroupHash      string                         `json:"groupHash"`
	Phase          string                         `json:"phase"`
	Unit           DistributedUnit                `json:"unit"`
	Secrets        map[string]string              `json:"secrets,omitempty"`
	RegistryAccess map[string]pipeline.Credential `json:"registryAccess,omitempty"`
}

// Maintenance freezes the source inventory and primary effect before any
// participant stops writers. Target is present only for reviewed data cutovers.
type Maintenance struct {
	Restore       *RestoreCutover `json:"restore,omitempty"`
	Kind          string          `json:"kind"`
	ResourceID    uuid.UUID       `json:"resourceId"`
	RunID         uuid.UUID       `json:"runId,omitempty"`
	EffectHash    string          `json:"effectHash"`
	ExpectedState string          `json:"expectedState"`
	Target        *Plan           `json:"target,omitempty"`
}

func (c CrossHostCommand) Digest() string { c.Secrets = nil; c.RegistryAccess = nil; return Hash(c) }

type CrossHostReceipt struct {
	CheckpointHash string      `json:"checkpointHash,omitempty"`
	ParentID       uuid.UUID   `json:"parentId"`
	GroupHash      string      `json:"groupHash"`
	UnitID         uuid.UUID   `json:"unitId"`
	Phase          string      `json:"phase"`
	PlanHash       string      `json:"planHash"`
	Observation    Observation `json:"observation"`
}

func (p Plan) Runs(id uuid.UUID) bool {
	return p.Local == nil || len(p.Local.Instances[id.String()]) > 0
}
func (p Plan) LocalApplications() []domain.Application {
	if p.Local == nil {
		return p.Specification.Applications
	}
	out := []domain.Application{}
	for _, a := range p.Specification.Applications {
		if p.Runs(a.ID) {
			out = append(out, a)
		}
	}
	return out
}
func (p Plan) OutputEndpoint(id uuid.UUID, port int) (string, int) {
	if p.Local != nil && !p.Runs(id) {
		if port == 0 {
			for _, e := range p.Local.Outputs {
				if e.ResourceID == id {
					return e.Address, e.Port
				}
			}
		}
		e := p.Local.Outputs[fmt.Sprintf("%s:%d", id, port)]
		return e.Address, e.Port
	}
	return p.HostName(id), port
}
func (p Plan) SecretReferences() []uuid.UUID {
	if p.Local == nil {
		return domain.SecretReferences(p.Specification)
	}
	found := map[uuid.UUID]bool{}
	vars := func(values []domain.Variable) {
		for _, v := range values {
			if v.SecretVersionID != nil {
				found[*v.SecretVersionID] = true
			}
		}
	}
	vars(p.Specification.Variables)
	for _, a := range p.LocalApplications() {
		vars(a.Variables)
		if a.Postgres != nil {
			found[a.Postgres.PasswordSecretVersionID] = true
		}
		for _, b := range p.Specification.Bindings {
			if b.TargetID != a.ID {
				continue
			}
			var source domain.Application
			for _, v := range p.Specification.Applications {
				if v.ID == b.SourceID {
					source = v
				}
			}
			output, ok := domain.BindingOutput(source, b.Output)
			if !ok {
				continue
			}
			if output.Type == "url" && source.Postgres != nil && output.Name == "URL" {
				found[source.Postgres.PasswordSecretVersionID] = true
			}
			if output.Type == "variable" {
				if v, ok := domain.OutputVariable(p.Specification, source, output.Variable); ok && v.SecretVersionID != nil {
					found[*v.SecretVersionID] = true
				}
			}
		}
	}
	out := []uuid.UUID{}
	for id := range found {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
func (p Plan) validateLocal() error {
	bad := errors.New("invalid local cross-host projection")
	l := p.Local
	if l == nil || p.Distributed != nil || p.Specification.Placement == nil || l.GroupID == uuid.Nil || l.Network.Validate() != nil || l.Network.ServerID != p.ServerID || l.Network.NetworkID != p.Specification.Placement.NetworkID || l.Network.Disabled {
		return bad
	}
	apps := map[string]domain.Application{}
	used := map[uuid.UUID]bool{p.UnitID: true}
	for _, a := range p.Specification.Applications {
		apps[a.ID.String()] = a
		used[a.ID] = true
	}
	for key, ids := range l.Instances {
		a, ok := apps[key]
		if !ok || len(ids) == 0 || len(ids) > domain.MaxReplicas {
			return bad
		}
		for _, id := range ids {
			legacy := l.Primary && id == a.ID
			for _, group := range p.Specification.Placement.Applications {
				if group.ApplicationID == a.ID {
					legacy = false
				}
			}
			if id == uuid.Nil || used[id] && !legacy {
				return bad
			}
			used[id] = true
		}
		if !l.Primary && (a.ServiceKind() != "web" || a.Postgres != nil || len(a.Mounts) > 0 || a.Health == nil) {
			return bad
		}
	}
	for key, request := range l.Requests {
		a, ok := apps[key]
		if !ok || !p.Runs(a.ID) || request.CPUMillis < 100 || request.CPUMillis > 16000 || request.MemoryMiB < 64 || request.MemoryMiB > 65536 {
			return bad
		}
	}
	peers := map[uuid.UUID]hostnetwork.Peer{}
	for _, peer := range l.Network.Peers {
		peers[peer.ServerID] = peer
	}
	check := func(e PrivateEndpoint) bool {
		a, ok := apps[e.ResourceID.String()]
		if !ok || e.UnitID == uuid.Nil || e.InstanceID == uuid.Nil || e.Port < 1 || e.Port > 65535 || e.ContainerPort < 1 || e.ContainerPort > 65535 {
			return false
		}
		if e.ServerID == p.ServerID {
			return e.UnitID == p.UnitID && slices.Contains(p.InstanceIDs(a.ID), e.InstanceID) && e.Address == ServiceName(e.InstanceID) && e.Port == e.ContainerPort
		}
		peer, ok := peers[e.ServerID]
		return ok && peer.Address == e.Address && e.Port >= hostnetwork.FirstEndpointPort && e.Port <= hostnetwork.LastEndpointPort
	}
	for key, e := range l.Outputs {
		if key != fmt.Sprintf("%s:%d", e.ResourceID, e.ContainerPort) || !check(e) {
			return bad
		}
	}
	for key, endpoints := range l.Backends {
		a, ok := apps[key]
		if !ok || !l.Primary || len(endpoints) == 0 || len(endpoints) > domain.MaxReplicas {
			return bad
		}
		seen := map[uuid.UUID]bool{}
		for _, e := range endpoints {
			if e.ResourceID != a.ID || !check(e) || seen[e.InstanceID] {
				return bad
			}
			seen[e.InstanceID] = true
		}
	}
	for _, a := range p.LocalApplications() {
		matched := false
		for _, group := range p.Specification.Placement.Applications {
			if group.ApplicationID == a.ID {
				matched = true
				if l.Requests[a.ID.String()] != (ResourceRequest{CPUMillis: group.CPUMillis, MemoryMiB: group.MemoryMiB}) {
					return bad
				}
			}
		}
		if !l.Primary && !matched {
			return bad
		}
		for _, b := range p.Specification.Bindings {
			if b.TargetID != a.ID || p.Runs(b.SourceID) {
				continue
			}
			for _, source := range p.Specification.Applications {
				if source.ID == b.SourceID {
					output, ok := domain.BindingOutput(source, b.Output)
					if !ok {
						return bad
					}
					if output.Type == "address" || output.Type == "port" || output.Type == "url" {
						host, port := p.OutputEndpoint(source.ID, output.Port)
						if host == "" || port == 0 {
							return bad
						}
					}
				}
			}
		}
	}
	for _, e := range l.Network.Exports {
		if e.UnitID == p.UnitID && !slices.Contains(p.InstanceIDs(e.ResourceID), e.InstanceID) && !slices.Contains(l.PriorInstances[e.ResourceID.String()], e.InstanceID) {
			return bad
		}
	}
	return nil
}
func (p Plan) validateDistributed(resolved bool) error {
	bad := errors.New("invalid coordinated cross-host plan")
	d := p.Distributed
	if d == nil || p.Local != nil || p.Specification.Placement == nil || d.NetworkID != p.Specification.Placement.NetworkID || d.NetworkGeneration < 1 || p.ServerID != p.Specification.Placement.IngressServerID || len(d.Units) < 1 || len(d.Units) > 4 {
		return bad
	}
	hosts := map[uuid.UUID]bool{}
	units := map[uuid.UUID]bool{}
	counts := map[uuid.UUID]map[uuid.UUID]int{}
	primary := 0
	for _, unit := range d.Units {
		u := unit.Plan
		if u.ID == p.ID || u.Local == nil || u.Local.GroupID != p.ID || u.EnvironmentID != p.EnvironmentID || u.RevisionID != p.RevisionID || Hash(u.Specification) != Hash(p.Specification) || hosts[u.ServerID] || units[u.UnitID] || u.Local.Network.Generation != d.NetworkGeneration || unit.CPUMillis < 0 || unit.MemoryMiB < 0 || len(unit.InventoryHash) != 64 {
			return bad
		}
		if err := u.Validate(resolved); err != nil {
			return err
		}
		hosts[u.ServerID] = true
		units[u.UnitID] = true
		if u.Local.Primary {
			primary++
			if u.ServerID != p.ServerID || u.UnitID != p.UnitID {
				return bad
			}
		}
		var cpu, mem int64
		for _, a := range u.LocalApplications() {
			if counts[a.ID] == nil {
				counts[a.ID] = map[uuid.UUID]int{}
			}
			count := len(u.InstanceIDs(a.ID))
			counts[a.ID][u.ServerID] = count
			if request, ok := u.Local.Requests[a.ID.String()]; ok {
				cpu += int64(request.CPUMillis * count)
				mem += int64(request.MemoryMiB * count)
			}
		}
		if cpu != unit.CPUMillis || mem != unit.MemoryMiB {
			return bad
		}
	}
	if primary != 1 {
		return bad
	}
	distributed := map[uuid.UUID]bool{}
	for _, group := range p.Specification.Placement.Applications {
		distributed[group.ApplicationID] = true
		for _, target := range group.Targets {
			if counts[group.ApplicationID][target.ServerID] != target.Count {
				return bad
			}
			delete(counts[group.ApplicationID], target.ServerID)
		}
		if len(counts[group.ApplicationID]) != 0 {
			return bad
		}
	}
	for _, a := range p.Specification.Applications {
		if !distributed[a.ID] && (len(counts[a.ID]) != 1 || counts[a.ID][p.ServerID] != a.ReplicaCount()) {
			return bad
		}
	}
	return nil
}
func endpointAddress(e PrivateEndpoint) string {
	if ip, err := netip.ParseAddr(e.Address); err == nil {
		return netip.AddrPortFrom(ip, uint16(e.Port)).String()
	}
	return e.Address + ":" + strconv.Itoa(e.Port)
}

func (p Plan) ValidateLocalObservation(o Observation) error {
	return p.validateLocalInventory(o, true)
}

func (p Plan) ValidateMaintenanceObservation(o Observation) error {
	return p.validateLocalInventory(o, false)
}

func (p Plan) validateLocalInventory(o Observation, running bool) error {
	if o.OmittedResources != 0 || len(o.Resources) != len(p.LocalApplications()) {
		return errors.New("incomplete inventory")
	}
	seen := map[uuid.UUID]bool{}
	for _, res := range o.Resources {
		ids := p.InstanceIDs(res.ID)
		if len(ids) == 0 || seen[res.ID] || len(ids) != len(res.Instances) || res.Image != p.Images[res.ID.String()] || running && res.State != "running" && res.State != "healthy" {
			return errors.New("invalid selected resource")
		}
		seen[res.ID] = true
		instances := map[uuid.UUID]bool{}
		for _, v := range res.Instances {
			if !slices.Contains(ids, v.InstanceID) || instances[v.InstanceID] || running && v.State != "running" && v.State != "healthy" {
				return errors.New("invalid selected instance")
			}
			instances[v.InstanceID] = true
		}
	}
	return nil
}

func (p Plan) PrimaryPlan() Plan {
	if p.Distributed != nil {
		for _, unit := range p.Distributed.Units {
			if unit.Plan.Local.Primary {
				return unit.Plan
			}
		}
	}
	return p
}
func (p Plan) VisualPlanID() uuid.UUID {
	if p.Local != nil {
		return p.Local.GroupID
	}
	return p.ID
}
