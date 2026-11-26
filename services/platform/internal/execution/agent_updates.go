package execution

import (
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// An agent refuses a release whose minimum version is below that of the release it runs, its
// floor. Management reads that floor from the catalog entry for the version and architecture the
// agent reports, and sends only releases at or above it, so that an update the agent would refuse
// never reaches the host. When the agent's release isn't in the catalog, its own version stands
// in for the floor: no release's minimum version is later than its version, so every release at
// or above it is one the agent installs. That refuses some releases the agent would accept rather
// than offer one it would refuse. With no valid version, nothing bounds the floor, and management
// sends nothing.

// agentUpdates is what management offers to update an agent with these capabilities to.
func agentUpdates(catalog []api.AgentRelease, cap api.AgentCapabilities) api.AgentUpdates {
	arch := api.AgentReleaseArch(cap.Arch)
	floor, ok := agentrelease.RunningFloor(catalog, cap.Version, arch)
	out := api.AgentUpdates{Basis: api.AgentUpdatesBasisUnknown, Versions: []string{}}
	if !ok {
		return out
	}
	out.Basis = api.AgentUpdatesBasisVersion
	if floor.Catalogued {
		out.Basis = api.AgentUpdatesBasisCatalog
	}
	out.MinimumVersion = &floor.MinimumVersion
	for _, r := range catalog {
		if r.Arch == arch && r.Version != cap.Version && agentrelease.CompareVersions(r.MinimumVersion, floor.MinimumVersion) >= 0 {
			out.Versions = append(out.Versions, r.Version)
		}
	}
	return out
}

// updateRelease is the catalog release named version that management sends an agent with these
// capabilities, or why it sends none.
func updateRelease(catalog []api.AgentRelease, cap api.AgentCapabilities, version string) (*api.AgentRelease, error) {
	arch := api.AgentReleaseArch(cap.Arch)
	var release *api.AgentRelease
	for i := range catalog {
		if r := &catalog[i]; r.Version == version && r.Arch == arch {
			release = r
			break
		}
	}
	if release == nil || release.Version == cap.Version {
		return nil, domain.Invalid("version")
	}
	floor, ok := agentrelease.RunningFloor(catalog, cap.Version, arch)
	switch {
	case !ok:
		return nil, &domain.Error{Code: "invalid_input", Fields: []string{"version"},
			Message: "This server's agent reports no release version, so management can't tell which releases it accepts. Update it with the local procedure."}
	case agentrelease.CompareVersions(release.MinimumVersion, floor.MinimumVersion) >= 0:
		return release, nil
	case floor.Catalogued:
		return nil, &domain.Error{Code: "invalid_input", Fields: []string{"version"},
			Message: fmt.Sprintf("This server's agent installs only releases whose minimum version is at least %s, that of the release it runs. Choose one of those.", floor.MinimumVersion)}
	default:
		return nil, &domain.Error{Code: "invalid_input", Fields: []string{"version"},
			Message: fmt.Sprintf("Release %s, which this server runs, isn't in the catalog, so it gets only releases whose minimum version is at least that. Add its manifest, or update it locally.", cap.Version)}
	}
}
