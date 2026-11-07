package catalog

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestRecipesUseValidatedVisualAndComposeSubset(t *testing.T) {
	for _, recipe := range Recipes() {
		t.Run(recipe.ID, func(t *testing.T) {
			raw, _ := stablejson.Marshal(recipe.Specification)
			if _, _, err := domain.ParseSpec(raw); err != nil {
				t.Fatal(err)
			}
			p := deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: recipe.Specification, Images: map[string]string{}, ExpiresAt: time.Now().Add(time.Hour)}
			secrets := map[string]string{}
			for _, app := range p.Specification.Applications {
				p.Images[app.ID.String()] = *app.Source.Image
				if app.Postgres != nil {
					secrets[app.Postgres.PasswordSecretVersionID.String()] = "catalog-fixture-password"
				}
			}
			p = p.WithHash()
			raw, err := deployment.Compose(p, secrets, false)
			if err != nil {
				t.Fatal(err)
			}
			var source map[string]any
			if err := json.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			// The input subset excludes platform-owned runtime identities and external
			// resources. Strip those projections, retaining each recipe's app settings.
			delete(source, "name")
			source["networks"] = map[string]any{"unit": map[string]any{}}
			for name := range source["volumes"].(map[string]any) {
				source["volumes"].(map[string]any)[name] = map[string]any{}
			}
			for _, service := range source["services"].(map[string]any) {
				s := service.(map[string]any)
				delete(s, "container_name")
				delete(s, "labels")
				s["networks"] = []string{"unit"}
			}
			// The runtime names its own services, and the same names are refused as input.
			if _, err := composeproject.Load(context.Background(), inputBundle(source), nil); err == nil {
				t.Fatal("a file declaring the runtime's service names was accepted")
			}
			authored := map[string]string{}
			for name := range source["services"].(map[string]any) {
				authored[name] = fmt.Sprintf("app-%d", len(authored))
			}
			services := map[string]any{}
			for name, service := range source["services"].(map[string]any) {
				s := service.(map[string]any)
				if dependencies, ok := s["depends_on"].(map[string]any); ok {
					renamed := map[string]any{}
					for dependency, condition := range dependencies {
						renamed[authored[dependency]] = condition
					}
					s["depends_on"] = renamed
				}
				services[authored[name]] = s
			}
			source["services"] = services
			if _, err := composeproject.Load(context.Background(), inputBundle(source), nil); err != nil {
				t.Fatal("recipe exceeds supported Compose input subset", err)
			}
			// A recipe is a visual specification, whose compiled output no check refuses.
			if err := domain.ValidateNewDraft(recipe.Specification, domain.Specification{}); err != nil {
				t.Fatal("a recipe could not become a draft", err)
			}
			if len(recipe.Specification.Routes) != 0 {
				t.Fatal("recipe publishes a service without review")
			}
		})
	}
}

func inputBundle(source map[string]any) composeproject.Bundle {
	raw, _ := stablejson.Marshal(source)
	return composeproject.Bundle{Namespace: uuid.New(), Entrypoints: []string{"compose.json"}, Files: []composeproject.File{{Path: "compose.json", Content: string(raw)}}}
}
