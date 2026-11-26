package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// An agent refuses a release whose minimum version is below that of the release it runs.
// Management reads that floor from the catalog and neither offers nor sends such a release, so
// the refusal never reaches the host. When it can't read the floor exactly, it offers only what
// the agent certainly installs.
func TestAgentUpdatesKeepTheAgentFloor(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	release := func(version, minimum string, arch api.AgentReleaseArch) api.AgentRelease {
		sum := sha256.Sum256([]byte(version + string(arch)))
		return api.AgentRelease{Version: version, MinimumVersion: minimum, Commit: agentFixtureCommit, Protocol: 1, Arch: arch, Size: 1, Sha256: hex.EncodeToString(sum[:])}
	}
	f.execution.Releases = []api.AgentRelease{
		release("0.1.0", "0.0.0", "arm64"),
		release("0.2.0", "0.1.0", "arm64"),
		release("0.2.1", "0.1.0", "arm64"),
		release("0.3.0", "0.3.0", "arm64"),
		release("0.3.1", "0.0.0", "amd64"),
	}
	enroll := func(version string) uuid.UUID {
		t.Helper()
		enrolled, input, _ := enrollment(t, f, b, ws, "deployment")
		input.Capabilities.Version = version
		if _, e := f.execution.Enroll(context.Background(), acknowledging(input), "test"); e != nil {
			t.Fatal(e)
		}
		return enrolled.Server.Id
	}
	catalogued, uncatalogued, unknown := enroll("0.2.0"), enroll("0.2.5"), enroll("test")
	base := "/api/v1/workspaces/" + ws.String()
	var servers []api.Server
	b.call("GET", base+"/servers", nil, 200, &servers)
	minimum := func(v string) *string { return &v }
	for id, want := range map[uuid.UUID]api.AgentUpdates{
		// The floor is the running release's minimum version: 0.1.0 is below it, and 0.3.1 is for
		// another architecture.
		catalogued: {Basis: "catalog", MinimumVersion: minimum("0.1.0"), Versions: []string{"0.2.1", "0.3.0"}},
		// The running release isn't in the catalog, so its version bounds the floor.
		uncatalogued: {Basis: "version", MinimumVersion: minimum("0.2.5"), Versions: []string{"0.3.0"}},
		unknown:      {Basis: "unknown", Versions: []string{}},
	} {
		i := slices.IndexFunc(servers, func(s api.Server) bool { return s.Id == id })
		if i < 0 || servers[i].AgentUpdates == nil {
			t.Fatalf("server %s has no agent updates", id)
		}
		got := *servers[i].AgentUpdates
		if got.Basis != want.Basis || !slices.Equal(got.Versions, want.Versions) || (got.MinimumVersion == nil) != (want.MinimumVersion == nil) || (got.MinimumVersion != nil && *got.MinimumVersion != *want.MinimumVersion) {
			t.Errorf("server %s offers %+v, want %+v", id, got, want)
		}
	}
	operations := func() int {
		t.Helper()
		var all []api.Operation
		b.call("GET", base+"/operations", nil, 200, &all)
		return len(all)
	}
	refused := func(server uuid.UUID, version, message string) {
		t.Helper()
		var failure api.Error
		if e := json.Unmarshal(b.call("POST", base+"/servers/"+server.String()+"/update", api.UpdateAgent{Version: version}, 400, nil), &failure); e != nil {
			t.Fatal(e)
		}
		if failure.Code != "invalid_input" || failure.Message != message || failure.Fields == nil || !slices.Equal(*failure.Fields, []string{"version"}) {
			t.Errorf("update of %s to %s: %+v, want %q", server, version, failure, message)
		}
	}
	refused(catalogued, "0.1.0", "This server's agent installs only releases whose minimum version is at least 0.1.0, that of the release it runs. Choose one of those.")
	refused(uncatalogued, "0.2.1", "Release 0.2.5, which this server runs, isn't in the catalog, so it gets only releases whose minimum version is at least that. Add its manifest, or update it locally.")
	refused(unknown, "0.3.0", "This server's agent reports no release version, so management can't tell which releases it accepts. Update it with the local procedure.")
	if n := operations(); n != 0 {
		t.Fatalf("a refused update made %d operations", n)
	}
	// Releases at or above the floor are sent, including one that raises it.
	for server, version := range map[uuid.UUID]string{catalogued: "0.2.1", uncatalogued: "0.3.0"} {
		var op api.Operation
		b.call("POST", base+"/servers/"+server.String()+"/update", api.UpdateAgent{Version: version}, 201, &op)
		if op.Kind != "update" || op.ServerId != server {
			t.Fatalf("update of %s to %s: %+v", server, version, op)
		}
	}
}
