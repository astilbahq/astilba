package pipeline

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The allowlist is the one way an owner widens what build code reaches, so it must never
// open metadata, loopback or anything not private, whatever form an entry takes.
func TestParseEgressRule(t *testing.T) {
	for _, c := range []struct {
		prefix string
		port   int
		want   string
	}{
		{"10.0.4.12", 443, "10.0.4.12/32"},
		{"10.0.4.12/24", 8081, "10.0.4.0/24"},
		{"192.168.1.0/24", 22, "192.168.1.0/24"},
		{"100.64.1.2", 443, "100.64.1.2/32"},
		{"169.254.169.254", 80, ""},
		{"169.254.0.0/16", 80, ""},
		{"100.100.100.200", 80, ""},
		{"100.64.0.0/10", 443, ""},
		{"168.63.129.16", 80, ""},
		{"127.0.0.1", 5432, ""},
		{"8.8.8.8", 53, ""},
		{"10.0.0.0/7", 443, ""},
		{"0.0.0.0/0", 443, ""},
		{"10.0.4.12", 0, ""},
		{"10.0.4.12", 65536, ""},
		{"fd00:ec2::254", 80, ""},
		{"::ffff:10.0.4.12", 443, ""},
		{"mirror.lan", 443, ""},
	} {
		rule, err := ParseEgressRule(c.prefix, c.port)
		if c.want == "" && err == nil || c.want != "" && (err != nil || rule.Prefix != c.want) {
			t.Errorf("%s:%d gave %v, %v; want %q", c.prefix, c.port, rule, err, c.want)
		}
	}
}

func TestEgressCovers(t *testing.T) {
	current := []EgressRule{{"10.0.4.12/32", 443}, {"10.0.5.0/24", 8081}}
	if !EgressCovers(current, current[1:]) || !EgressCovers(current, nil) || !EgressCovers(current, []EgressRule{{"10.0.5.7/32", 8081}}) || !EgressCovers([]EgressRule{{"10.0.4.0/24", 443}}, current[:1]) {
		t.Fatal("a build within the current allowlist was refused")
	}
	if EgressCovers(current[1:], current) || EgressCovers(current, []EgressRule{{"10.0.4.12/32", 444}}) || EgressCovers(current, []EgressRule{{"10.0.0.0/16", 8081}}) {
		t.Fatal("a build kept a destination the owner removed")
	}
}

// Agents apply the build network to every build, so one from a controller that predates the
// marker runs; a marker or allowlist never rides on other work, and an unknown marker stops.
func TestBuildNetworkMarker(t *testing.T) {
	build := Command{Action: "build", Commit: strings.Repeat("a", 40), Branch: "main", RevisionID: uuid.New(), UnitID: uuid.New(), AppID: uuid.New(), InputHash: strings.Repeat("b", 64), Connection: Connection{Repository: "team/app"}, Build: &BuildConfig{ConnectionID: uuid.New(), Branch: "main", RunnerID: uuid.New(), Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}}
	for marker, valid := range map[string]bool{"": true, BuildNetworkV1: true, "v2": false} {
		build.BuildNetwork = marker
		if err := build.Validate(); (err == nil) != valid {
			t.Errorf("build with marker %q: %v", marker, err)
		}
	}
	other := build
	other.Action, other.Build, other.Source = "import", nil, &Source{ConnectionID: uuid.New(), Branch: "main", RunnerID: uuid.New(), Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	other.BuildNetwork = BuildNetworkV1
	if other.Validate() == nil {
		t.Error("an import carried the build network marker")
	}
	other.BuildNetwork, other.BuildEgress = "", []EgressRule{{"10.0.4.12/32", 443}}
	if other.Validate() == nil {
		t.Error("an import carried a build allowlist")
	}
}
