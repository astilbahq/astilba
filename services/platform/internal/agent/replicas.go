package agent

import (
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// Keep logical resource identity while retaining every installed instance in
// observations. The journal's expected fingerprints remain keyed by instance.
func aggregateReplicaResources(p deployment.Plan, rows []deployment.Resource, installed bool) []deployment.Resource {
	byInstance := map[uuid.UUID]deployment.Resource{}
	for _, row := range rows {
		if len(row.Instances) == 1 {
			byInstance[row.Instances[0].InstanceID] = row
		}
	}
	managed := map[uuid.UUID]bool{}
	out := []deployment.Resource{}
	for _, app := range p.Specification.Applications {
		managed[app.ID] = true
		group := deployment.Resource{ID: app.ID, Kind: "service", Image: p.Images[app.ID.String()]}
		running, healthy, present := 0, 0, 0
		for _, id := range p.InstanceIDs(app.ID) {
			member := deployment.InstanceState{InstanceID: id, State: "missing"}
			if row, ok := byInstance[id]; ok && row.ID == app.ID {
				present++
				member.State = row.State
				if group.Image != "" && row.Image != group.Image {
					member.State = "image-mismatch"
					group.Drift = true
				}
				if member.State == "running" || member.State == "healthy" {
					running++
				}
				if member.State == "healthy" {
					healthy++
				}
				group.Drift = group.Drift || row.Drift
				group.MemoryBytes += row.MemoryBytes
				group.Logs += row.Logs
			} else if installed {
				group.Drift = true
			}
			group.Instances = append(group.Instances, member)
		}
		group.State = "degraded"
		if present == 0 {
			group.State = "missing"
		} else if running == len(group.Instances) {
			group.State = "running"
			if healthy == running {
				group.State = "healthy"
			}
		}
		group.Note = fmt.Sprintf("%d of %d instances running.", running, len(group.Instances))
		out = append(out, group)
	}
	for _, row := range rows {
		if !managed[row.ID] {
			out = append(out, row)
		}
	}
	return out
}
