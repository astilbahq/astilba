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

// elevatedServices are the services of a Compose project that review treats as having host-level
// access: privileged and cap_add services, and every service that shares a named volume or a
// network with one. A peer can drive a privileged service through what they share, such as a
// Docker API on their network or files it runs from their volume, so its changes reach the host
// as well.
func elevatedServices(project *composeproject.Project, spec domain.Specification) map[string]bool {
	out := map[string]bool{}
	if project == nil {
		return out
	}
	shared := map[string]bool{}
	for name, service := range project.Model.Services {
		if elevatedService(service) {
			out[name] = true
			for _, reach := range serviceReach(spec, name, service) {
				shared[reach] = true
			}
		}
	}
	for name, service := range project.Model.Services {
		for _, reach := range serviceReach(spec, name, service) {
			out[name] = out[name] || shared[reach]
		}
	}
	return out
}

// serviceReach names the named volumes and networks a service shares with others in its project.
func serviceReach(spec domain.Specification, name string, service types.ServiceConfig) []string {
	reach := []string{}
	for _, mount := range service.Volumes {
		if mount.Type == "volume" {
			reach = append(reach, "volume:"+mount.Source)
		}
	}
	for _, network := range serviceNetworks(spec, name, service) {
		reach = append(reach, "network:"+network)
	}
	return reach
}

// serviceNetworks names the networks a service runs on. Loading puts a service that names none
// on the project's default network, and deployment also attaches a route's target there, so
// its proxy can reach it (see deployment.Plan's Compose rendering).
func serviceNetworks(spec domain.Specification, name string, service types.ServiceConfig) []string {
	networks := slices.Collect(maps.Keys(service.Networks))
	id := composeproject.ResourceID(spec.Compose.Namespace, "service", name)
	if !slices.Contains(networks, "default") && slices.ContainsFunc(spec.Routes, func(r domain.Route) bool { return r.TargetID == id }) {
		networks = append(networks, "default")
	}
	slices.Sort(networks)
	return networks
}

// elevatedReview is an elevated Compose service, see elevatedServices, of the reviewed
// specification or the next one.
type elevatedReview struct {
	Name string
	// Unchanged holds when the service is in both specifications with identical inputs: its
	// definition, the variables it interpolates, its files, configs and secret versions, and the
	// networks, volumes and routes that reach it. Its image must also resolve as reviewed; see
	// resolvedImageStands.
	Unchanged bool
}

// reviewElevatedServices compares every elevated service, in either specification,
// between the reviewed (applied) specification and the next one, keyed by service ID. A visual
// specification has none, so on a first deployment or a switch to or from Compose files every
// such service is reported changed. A service that becomes elevated, or stops being, is changed.
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
	oldElevated, nextElevated := elevatedServices(oldProject, reviewed), elevatedServices(nextProject, next)
	for _, name := range serviceNames(oldProject, nextProject) {
		if !oldElevated[name] && !nextElevated[name] {
			continue
		}
		_, nowOK := service(nextProject, name)
		unchanged := oldElevated[name] && nextElevated[name] && reflect.DeepEqual(composeServiceInputs(oldProject, name, reviewed, true), composeServiceInputs(nextProject, name, next, true))
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

// Elevation is what review treats as having host-level access in a specification: its
// elevated services, see elevatedServices, by resource ID, and the runtime names of the
// networks and named volumes its privileged and cap_add services reach. A container on one of
// those can drive such a service, whichever plan created it. A visual specification has none.
// Terminal sessions and scheduled jobs are refused in all of them, by management and again by
// the agent.
type Elevation struct {
	Services map[uuid.UUID]bool
	Reach    map[string]bool
}

// ElevationOf finds a specification's Elevation.
func ElevationOf(spec domain.Specification) (Elevation, error) {
	out := Elevation{Services: map[uuid.UUID]bool{}, Reach: map[string]bool{}}
	if spec.Compose == nil {
		return out, nil
	}
	project, err := deployment.SourceProject(deployment.Plan{Specification: spec}, nil)
	if err != nil {
		return Elevation{}, err
	}
	namespace := spec.Compose.Namespace
	for name, elevated := range elevatedServices(project, spec) {
		if elevated {
			out.Services[composeproject.ResourceID(namespace, "service", name)] = true
		}
	}
	for name, service := range project.Model.Services {
		if !elevatedService(service) {
			continue
		}
		for _, mount := range service.Volumes {
			if mount.Type == "volume" {
				out.Reach[deployment.VolumeName(composeproject.ResourceID(namespace, "volume", mount.Source))] = true
			}
		}
		for _, network := range serviceNetworks(spec, name, service) {
			out.Reach[deployment.SourceNetworkName(namespace, composeproject.ResourceID(namespace, "network", network), network)] = true
		}
	}
	return out, nil
}
