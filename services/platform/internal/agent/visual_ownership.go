package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
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

// labelPostgresMajor records on a data volume the PostgreSQL major version whose server first ran
// on it. Labels are fixed when a volume is created, so a volume created before this label existed
// carries none, and management's own record of the volume is then the only check.
const labelPostgresMajor = "com.astilba.postgres-major"

// postgresMajors names the major each PostgreSQL service in the plan runs on its data volume.
func postgresMajors(p deployment.Plan) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	for _, a := range p.Specification.Applications {
		if a.Postgres != nil && a.Source.Image != nil && len(a.Mounts) == 1 {
			if major := domain.PostgresMajor(*a.Source.Image); major != "" {
				out[a.Mounts[0].VolumeID] = major
			}
		}
	}
	return out
}

// volumeLabels is a plan's data volume's labels, with the PostgreSQL major that will run on it.
func volumeLabels(p deployment.Plan, volume uuid.UUID) map[string]string {
	labels := visualLabels(p, volume)
	if major := postgresMajors(p)[volume]; major != "" {
		labels[labelPostgresMajor] = major
	}
	return labels
}

// postgresVolumesMatch refuses, before any effect, a plan that would start a PostgreSQL major on a
// data volume another major initialised. Unlike the journal's applied plan, the volume's label
// survives retirement and anything else that forgets what ran on it.
func (e *Engine) postgresVolumesMatch(ctx context.Context, p deployment.Plan) error {
	for volume, major := range postgresMajors(p) {
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if recorded := v.Volume.Labels[labelPostgresMajor]; recorded != "" && recorded != major {
			return errPostgresVersionChange
		}
	}
	return nil
}

var errPostgresVersionChange = errors.New("changing a PostgreSQL version requires a supported upgrade or new storage")
