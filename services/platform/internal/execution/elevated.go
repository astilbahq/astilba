package execution

import (
	"maps"
	"reflect"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/google/uuid"
)

// An owner's approval of a privileged or cap_add service covers exactly what it ran, so it stands
// only while the service is unchanged. Automatic deployment and manual plan preparation both
// decide that here, so the two cannot drift.

// elevatedService reports whether a Compose service has host-level access. Privileged mode and
// added capabilities are the only host permissions other than published ports that Compose
// sources may request.
func elevatedService(service types.ServiceConfig) bool {
	return service.Privileged || len(service.CapAdd) > 0
}

// elevatedReview is a privileged or cap_add Compose service of the reviewed specification or the
// next one.
type elevatedReview struct {
	Name string
	// Unchanged holds when the service is in both specifications with identical inputs: its
	// definition, the variables it interpolates, its files, configs and secret versions, and the
	// networks, volumes and routes that reach it. Its image must also resolve as reviewed; see
	// resolvedImageStands.
	Unchanged bool
}

// reviewElevatedServices compares every privileged or cap_add service, in either specification,
// between the reviewed (applied) specification and the next one, keyed by service ID. A visual
// specification has none, so on a first deployment or a switch to or from Compose files every
// such service is reported changed.
func reviewElevatedServices(reviewed, next domain.Specification) (map[uuid.UUID]elevatedReview, error) {
	load := func(spec domain.Specification) (*composeproject.Project, error) {
		if spec.Compose == nil {
			return nil, nil
		}
		return deployment.SourceProject(deployment.Plan{Specification: spec}, nil)
	}
	old, err := load(reviewed)
	if err != nil {
		return nil, err
	}
	now, err := load(next)
	if err != nil {
		return nil, err
	}
	return compareElevatedServices(old, now, reviewed, next), nil
}

// compareElevatedServices is reviewElevatedServices for the Compose projects already loaded from
// both specifications; a nil project is a specification without Compose files.
func compareElevatedServices(oldProject, nextProject *composeproject.Project, reviewed, next domain.Specification) map[uuid.UUID]elevatedReview {
	out := map[uuid.UUID]elevatedReview{}
	service := func(project *composeproject.Project, name string) (types.ServiceConfig, bool) {
		if project == nil {
			return types.ServiceConfig{}, false
		}
		s, ok := project.Model.Services[name]
		return s, ok
	}
	for _, name := range serviceNames(oldProject, nextProject) {
		old, oldOK := service(oldProject, name)
		now, nowOK := service(nextProject, name)
		if !(oldOK && elevatedService(old) || nowOK && elevatedService(now)) {
			continue
		}
		unchanged := oldOK && nowOK && reflect.DeepEqual(composeServiceInputs(oldProject, name, reviewed, true), composeServiceInputs(nextProject, name, next, true))
		namespace := reviewed.Compose
		if nowOK {
			namespace = next.Compose
		}
		out[composeproject.ResourceID(namespace.Namespace, "service", name)] = elevatedReview{Name: name, Unchanged: unchanged}
	}
	return out
}

// resolvedImageStands reports whether a service resolved to the image the reviewed deployment
// ran. Both sides must have one: see planImages.
func resolvedImageStands(reviewed, candidate map[string]string, id uuid.UUID) bool {
	return reviewed[id.String()] != "" && reviewed[id.String()] == candidate[id.String()]
}

// serviceNames lists the services of either project, sorted.
func serviceNames(projects ...*composeproject.Project) []string {
	names := []string{}
	for _, project := range projects {
		if project == nil {
			continue
		}
		for name := range maps.Keys(project.Model.Services) {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}
