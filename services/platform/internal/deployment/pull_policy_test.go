package deployment

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// pullPolicies is each compiled service's pull_policy, "" where it has none.
func pullPolicies(t *testing.T, compiled []byte) map[string]string {
	t.Helper()
	var project struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(compiled, &project); err != nil {
		t.Fatal(err)
	}
	policies := map[string]string{}
	for name, service := range project.Services {
		policy, _ := service["pull_policy"].(string)
		policies[name] = policy
	}
	return policies
}

// Only the agent puts images into Docker, within its bounds, so a host's Compose never pulls a
// service's image. A Compose source cannot set pull_policy itself. An export runs elsewhere
// and pulls as usual.
func TestComposeNeverPullsOnTheHost(t *testing.T) {
	visual := testPlan()
	visual.Images = map[string]string{}
	for _, app := range visual.Specification.Applications {
		visual.Images[app.ID.String()] = *app.Source.Image
	}
	visual = visual.WithHash()
	unit := uuid.New()
	image := "registry.example.test/web@sha256:" + strings.Repeat("a", 64)
	raw, _ := stablejson.Marshal(map[string]any{"version": 1, "applications": []any{}, "variables": []any{}, "compose": composeproject.Bundle{Namespace: unit, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: "services:\n  web:\n    image: " + image + "\n"}}}})
	spec, _, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	source := Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: unit, RevisionID: uuid.New(), Recipe: Recipe, Specification: spec, Images: map[string]string{composeproject.ResourceID(unit, "service", "web").String(): image}, Effects: []string{}}.WithHash()
	for name, p := range map[string]Plan{"visual": visual, "Compose source": source} {
		secrets := map[string]string{}
		for _, ref := range domain.SecretReferences(p.Specification) {
			secrets[ref.String()] = "value"
		}
		compiled, err := Compose(p, secrets, false)
		if err != nil {
			t.Fatal(name, err)
		}
		policies := pullPolicies(t, compiled)
		if len(policies) == 0 {
			t.Fatal(name, "compiled no services")
		}
		for service, policy := range policies {
			if policy != "never" {
				t.Errorf("%s: %s pulls %q on the host", name, service, policy)
			}
		}
		exported, err := Compose(p, nil, true)
		if err != nil {
			t.Fatal(name, err)
		}
		for service, policy := range pullPolicies(t, exported) {
			if policy != "" {
				t.Errorf("%s: exported %s has pull_policy %q", name, service, policy)
			}
		}
	}
}
