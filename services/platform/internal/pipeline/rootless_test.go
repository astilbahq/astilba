package pipeline

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Agents run every build rootless, so one from a controller that predates the marker runs; the
// marker never rides on other work, an unknown one stops, and an agent that drops it, as one that
// predates the rootless builder does, computes another digest and refuses the build.
func TestRootlessBuildMarker(t *testing.T) {
	build := Command{Action: "build", BuildNetwork: BuildNetworkV1, Commit: strings.Repeat("a", 40), Branch: "main", RevisionID: uuid.New(), UnitID: uuid.New(), AppID: uuid.New(), InputHash: strings.Repeat("b", 64), Connection: Connection{Repository: "team/app"}, Build: &BuildConfig{ConnectionID: uuid.New(), Branch: "main", RunnerID: uuid.New(), Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}}
	for marker, valid := range map[string]bool{"": true, RootlessBuildV1: true, "v2": false} {
		build.RootlessBuild = marker
		if err := build.Validate(); (err == nil) != valid {
			t.Errorf("build with marker %q: %v", marker, err)
		}
	}
	dropped := build
	dropped.RootlessBuild = ""
	if build.RootlessBuild = RootlessBuildV1; build.Digest() == dropped.Digest() {
		t.Error("an agent that drops the marker computes the same digest")
	}
	other := build
	other.Action, other.Build, other.BuildNetwork, other.Source = "import", nil, "", &Source{ConnectionID: uuid.New(), Branch: "main", RunnerID: uuid.New(), Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	if other.Validate() == nil {
		t.Error("an import carried the rootless builder marker")
	}
}
