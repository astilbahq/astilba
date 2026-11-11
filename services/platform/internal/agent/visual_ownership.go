package agent

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func visualLabels(p deployment.Plan, resource uuid.UUID) map[string]string {
	return map[string]string{labelServer: p.ServerID.String(), labelEnvironment: p.EnvironmentID.String(), "com.astilba.unit": p.UnitID.String(), labelResource: resource.String(), labelKind: "visual-v1"}
}
func visualOwned(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels["com.astilba.unit"] == p.UnitID.String() && labels[labelKind] == "visual-v1"
}
func (e *Engine) ownedContainers(ctx context.Context, p deployment.Plan) ([]container.Summary, error) {
	v, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelServer+"="+p.ServerID.String(), "com.astilba.unit="+p.UnitID.String(), labelKind+"=visual-v1")})
	return v.Items, err
}
func (e *Engine) activeContainers(ctx context.Context, p deployment.Plan) ([]container.Summary, error) {
	list, err := e.ownedContainers(ctx, p)
	if err != nil {
		return nil, err
	}
	out := []container.Summary{}
	names := map[string]string{}
	for _, app := range p.Specification.Applications {
		for _, instance := range p.InstanceIDs(app.ID) {
			names["/"+deployment.UnitName(instance)] = app.ID.String()
		}
	}
	for _, item := range list {
		for _, name := range item.Names {
			if resource, ok := names[name]; ok && resource == item.Labels[labelResource] {
				out = append(out, item)
				break
			}
		}
	}
	return out, nil
}
