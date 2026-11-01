package deployment

import (
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// Validate checks the report's shape. Membership and required inventory depend
// on the frozen plan and execution phase and remain the caller's responsibility.
func (o Observation) Validate(now time.Time) error {
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now.Add(time.Minute)) {
		return domain.Invalid("observedAt")
	}
	if len(o.Resources) > 40 || o.OmittedResources < 0 || len(o.StateHash) > 64 || len(o.Images) > 20 {
		return domain.Invalid("observation")
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
