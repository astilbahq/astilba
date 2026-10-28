package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Compose also discovers replicas and interrupted replacements through its own
// project/service labels. Check that whole selected set before it can recreate
// or remove a container, including collisions without a reviewed volume mount.
func (e *Engine) requireOwnedComposeServices(ctx context.Context, p deployment.Plan, apps []domain.Application) error {
	for _, app := range apps {
		services := map[string]bool{}
		for _, id := range p.InstanceIDs(app.ID) {
			services[deployment.ServiceName(id)] = true
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil && !errdefs.IsNotFound(err) {
				return err
			}
			if err == nil && (!visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != app.ID.String()) {
				return errors.New("a selected Compose container has conflicting Astilba ownership")
			}
		}
		all, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", "com.docker.compose.project="+composeApplicationProject(p, app.ID))})
		if err != nil {
			return err
		}
		for _, c := range all.Items {
			if services[c.Labels["com.docker.compose.service"]] && (!visualOwned(c.Labels, p) || c.Labels[labelResource] != app.ID.String()) {
				return errors.New("Compose discovery includes a container with conflicting Astilba ownership")
			}
		}
	}
	return nil
}
