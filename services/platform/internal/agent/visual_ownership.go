package agent

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
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
	if a, ok := postgresOn(p, volume); ok && a.Postgres.Owner() {
		labels[labelPostgresRoles] = postgresOwnerRoles
	}
	return labels
}

// restoreTargetLabels are a fresh restore target's labels. A logical PostgreSQL restore initialises
// the target with the release that restores it, in the owner model, so the target records that
// release's major and its role model, and a deployment that would start another major on it, or
// run its database user as the superuser, is refused like on any other data volume. Other
// restores pass no release.
func restoreTargetLabels(p deployment.Plan, target uuid.UUID, release domain.PostgresRelease) map[string]string {
	labels := visualLabels(p, target)
	if major := domain.PostgresMajor(release.Image); major != "" {
		labels[labelPostgresMajor] = major
		labels[labelPostgresRoles] = postgresOwnerRoles
	}
	return labels
}

// postgresVolumeMatches refuses to start a PostgreSQL major on a data volume another major
// initialised, and a database in a role model other than the volume's. Unlike the journal's
// applied plan, the volume's labels survive retirement and anything else that forgets what ran on
// it. A volume without the role model's label is from before the owner model, whose database
// user is its superuser.
func postgresVolumeMatches(labels map[string]string, p deployment.Plan, volume uuid.UUID) error {
	if recorded, major := labels[labelPostgresMajor], postgresMajors(p)[volume]; recorded != "" && major != "" && recorded != major {
		return errPostgresVersionChange
	}
	if a, ok := postgresOn(p, volume); ok && a.Postgres.Owner() != (labels[labelPostgresRoles] == postgresOwnerRoles) {
		return errPostgresRolesChange
	}
	return nil
}

var errPostgresRolesChange = errors.New("changing a database's role model requires new storage, such as an upgrade's or a restore's target")

var errPostgresVersionChange = errors.New("changing a PostgreSQL version requires a supported upgrade or new storage")

func upgradeOwned(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels["com.astilba.unit"] == p.UnitID.String()
}

// upgradeVolumeLabels identifies the target volume as the source unit's, and records the major and
// the role model the upgrade initialises on it.
func upgradeVolumeLabels(p upgrade.Plan) map[string]string {
	labels := visualLabels(p.Source, p.TargetVolumeID)
	target := p.Target()
	if major := postgresMajors(target)[p.TargetVolumeID]; major != "" {
		labels[labelPostgresMajor] = major
	}
	if a, ok := postgresOn(target, p.TargetVolumeID); ok && a.Postgres.Owner() {
		labels[labelPostgresRoles] = postgresOwnerRoles
	}
	return labels
}
