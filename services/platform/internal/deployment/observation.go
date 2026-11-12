package deployment

import (
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// Validate checks the report's shape. Membership and required inventory depend
// on the frozen plan and execution phase and remain the caller's responsibility.
type HostObservation struct {
	ServerID   uuid.UUID `json:"serverId"`
	UnitID     uuid.UUID `json:"unitId"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observedAt"`
}
type Observation struct {
	Hosts               []HostObservation `json:"hosts,omitempty"`
	ObservedAt          time.Time         `json:"observedAt"`
	Resources           []Resource        `json:"resources"`
	Images              map[string]string `json:"images,omitempty"`
	Applied             bool              `json:"applied"`
	StateHash           string            `json:"stateHash"`
	OmittedResources    int               `json:"omittedResources,omitempty"`
	ResourceDrift       bool              `json:"resourceDrift"`
	InfrastructureDrift bool              `json:"infrastructureDrift"`
	// HostChanges is what applying the resolved plan would change on the host itself, as its
	// agent sees it. HostChangesReported says the agent worked it out, so an empty list means
	// none; older agents leave both out, and report nothing they would change.
	HostChanges         []HostChange `json:"hostChanges,omitempty"`
	HostChangesReported bool         `json:"hostChangesReported,omitempty"`
}

func (o Observation) Validate(now time.Time) error {
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now.Add(time.Minute)) {
		return domain.Invalid("observedAt")
	}
	if len(o.Resources) > 40 || o.OmittedResources < 0 || len(o.StateHash) > 64 || len(o.Images) > 20 || len(o.HostChanges) > 3 {
		return domain.Invalid("observation")
	}
	kinds := map[string]bool{}
	for _, change := range o.HostChanges {
		switch {
		case kinds[change.Kind]:
			return domain.Invalid("observation.hostChanges")
		case change.Kind == HostChangePorts:
			if len(change.Ports) == 0 || len(change.Ports) > 64 {
				return domain.Invalid("observation.hostChanges")
			}
			for _, port := range change.Ports {
				if port < 1 || port > 65535 {
					return domain.Invalid("observation.hostChanges")
				}
			}
		case change.Kind == HostChangeCertificateEmail || change.Kind == HostChangeManagedEgress:
			if len(change.Ports) > 0 {
				return domain.Invalid("observation.hostChanges")
			}
		default:
			return domain.Invalid("observation.hostChanges")
		}
		kinds[change.Kind] = true
	}
	seen := make(map[uuid.UUID]bool, len(o.Resources))
	for _, resource := range o.Resources {
		if resource.ID == uuid.Nil || seen[resource.ID] || len(resource.Logs) > 2048 || len(resource.Note) > 256 || len(resource.Image) > 512 || len(resource.State) > 30 || len(resource.Instances) > domain.MaxReplicas {
			return domain.Invalid("observation.resources")
		}
		seen[resource.ID] = true
		members := make(map[uuid.UUID]bool, len(resource.Instances))
		for _, instance := range resource.Instances {
			if instance.InstanceID == uuid.Nil || members[instance.InstanceID] || len(instance.State) > 30 {
				return domain.Invalid("observation.instances")
			}
			members[instance.InstanceID] = true
		}
	}
	return nil
}
