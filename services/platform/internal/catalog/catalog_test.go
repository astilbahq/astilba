package catalog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestRecipesUseValidatedVisualAndComposeSubset(t *testing.T) {
	for _, recipe := range Recipes() {
		t.Run(recipe.ID, func(t *testing.T) {
			raw, _ := json.Marshal(recipe.Specification)
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
			raw, _ = json.Marshal(source)
			bundle := composeproject.Bundle{Namespace: uuid.New(), Entrypoints: []string{"compose.json"}, Files: []composeproject.File{{Path: "compose.json", Content: string(raw)}}}
			if _, err := composeproject.Load(context.Background(), bundle, nil); err != nil {
				t.Fatal("recipe exceeds supported Compose input subset", err)
			}
			if len(recipe.Specification.Routes) != 0 {
				t.Fatal("recipe publishes a service without review")
			}
		})
	}
}
