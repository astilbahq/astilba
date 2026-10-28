package domain

import "github.com/google/uuid"

// Placement belongs to the saved revision; a canvas connection never selects a host.
type Placement struct {
	NetworkID       uuid.UUID              `json:"networkId"`
	IngressServerID uuid.UUID              `json:"ingressServerId"`
	Applications    []ApplicationPlacement `json:"applications"`
}

type ApplicationPlacement struct {
	ApplicationID uuid.UUID         `json:"applicationId"`
	CPUMillis     int               `json:"cpuMillis"`
	MemoryMiB     int               `json:"memoryMiB"`
	Targets       []PlacementTarget `json:"targets"`
}

type PlacementTarget struct {
	ServerID uuid.UUID `json:"serverId"`
	Count    int       `json:"count"`
}

func (s Specification) ValidatePlacement() error {
	if s.Placement == nil {
		return nil
	}
	p := s.Placement
	if s.Compose != nil || p.NetworkID == uuid.Nil || p.IngressServerID == uuid.Nil || len(p.Applications) == 0 || len(p.Applications) > 20 {
		return Invalid("placement")
	}
	apps := map[uuid.UUID]Application{}
	for _, a := range s.Applications {
		apps[a.ID] = a
	}
	seen := map[uuid.UUID]bool{}
	hosts := map[uuid.UUID]bool{p.IngressServerID: true}
	for _, group := range p.Applications {
		a, ok := apps[group.ApplicationID]
		if !ok || seen[a.ID] || a.ServiceKind() != "web" || a.Postgres != nil || len(a.Mounts) != 0 || a.Health == nil || !a.Health.Valid() || group.CPUMillis < 100 || group.CPUMillis > 16000 || group.MemoryMiB < 64 || group.MemoryMiB > 65536 || len(group.Targets) == 0 || len(group.Targets) > 4 {
			return Invalid("placement.applications")
		}
		seen[a.ID] = true
		routed := false
		for _, r := range s.Routes {
			if r.TargetID == a.ID {
				if r.Protocol != "http" || r.Port != a.Health.Port {
					return Invalid("placement.routes")
				}
				routed = true
			}
		}
		if !routed {
			return Invalid("placement.routes")
		}
		for _, b := range s.Bindings {
			if b.SourceID == a.ID {
				return Invalid("placement.bindings")
			}
		}
		for _, other := range s.Applications {
			for _, id := range Dependencies(s, other) {
				if id == a.ID {
					return Invalid("placement.dependencies")
				}
			}
		}
		count := 0
		targets := map[uuid.UUID]bool{}
		for _, target := range group.Targets {
			if target.ServerID == uuid.Nil || targets[target.ServerID] || target.Count < 1 || target.Count > MaxReplicas {
				return Invalid("placement.targets")
			}
			targets[target.ServerID] = true
			hosts[target.ServerID] = true
			count += target.Count
		}
		if count != a.ReplicaCount() {
			return Invalid("placement.count")
		}
	}
	if len(hosts) > 4 {
		return Invalid("placement.servers")
	}
	return nil
}

func (s Specification) HasRemotePlacement() bool {
	if s.Placement == nil {
		return false
	}
	for _, application := range s.Placement.Applications {
		for _, target := range application.Targets {
			if target.ServerID != s.Placement.IngressServerID {
				return true
			}
		}
	}
	return false
}
