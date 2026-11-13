package catalog

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func route() *StageRoute {
	return &StageRoute{Domain: "tracker.example.com", TLS: "managed", AllowCIDRs: []string{"203.0.113.7/32"}}
}

func TestEveryEntryStagesUnderFreshIdentities(t *testing.T) {
	for _, recipe := range Recipes() {
		t.Run(recipe.ID, func(t *testing.T) {
			in := Stage{Name: "Copy"}
			if recipe.Route != nil {
				in.Route = route()
			}
			staged, err := recipe.Stage(in, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := stablejson.Marshal(staged.Specification)
			if _, _, err = domain.ParseSpec(raw); err != nil {
				t.Fatal(err)
			}
			placeholders, _ := stablejson.Marshal(recipe.Specification)
			for _, s := range recipe.Secrets {
				placeholders = append(placeholders, s.ID.String()...)
			}
			for _, id := range staged.Identities {
				if strings.Contains(string(placeholders), id.String()) {
					t.Fatal("staged a placeholder identity")
				}
			}
			for _, a := range recipe.Specification.Applications {
				if strings.Contains(string(raw), a.ID.String()) {
					t.Fatal("staged specification keeps a placeholder")
				}
			}
			for _, s := range recipe.Secrets {
				if strings.Contains(string(raw), s.ID.String()) {
					t.Fatal("staged specification keeps a placeholder secret")
				}
			}
			routes := 0
			if recipe.Route != nil {
				routes = 1
			}
			if len(staged.Generated) != len(recipe.Secrets) || len(staged.Specification.Routes) != routes {
				t.Fatal("staged the wrong secrets or routes")
			}
			for _, a := range staged.Specification.Applications {
				if !strings.HasPrefix(a.Name, "Copy") {
					t.Fatal("service keeps the entry's name", a.Name)
				}
			}
		})
	}
}

func TestKaneoStagesConnectedBehindAnAllowlist(t *testing.T) {
	kaneo, ok := Find("kaneo")
	if !ok {
		t.Fatal("no Kaneo template")
	}
	existing := uuid.New()
	password := kaneo.Specification.Applications[1].Postgres.PasswordSecretVersionID
	staged, err := kaneo.Stage(Stage{Name: "Tracker", Existing: map[uuid.UUID]uuid.UUID{password: existing}, Route: route()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := staged.Specification
	app, database := s.Applications[0], s.Applications[1]
	if app.Name != "Tracker" || database.Name != "Tracker database" || s.Volumes[0].Name != "Tracker database data" {
		t.Fatal("names", app.Name, database.Name, s.Volumes[0].Name)
	}
	if database.Postgres.PasswordSecretVersionID != existing || database.Postgres.Database != "kaneo" || *database.Source.Image != domain.PostgresImage {
		t.Fatal("database lost its chosen password or current image")
	}
	if len(staged.Generated) != 1 || staged.Generated[0].Name != "Tracker · AUTH_SECRET" || staged.Generated[0].Format != "hex" {
		t.Fatal("generated", staged.Generated)
	}
	value, err := staged.Generated[0].Value()
	if err != nil || len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
		t.Fatal("AUTH_SECRET is not 32 hex bytes", err)
	}
	variables := byName(app)
	if *variables["KANEO_CLIENT_URL"].Value != "https://tracker.example.com" || *variables["AUTH_SECRET"].SecretVersionID != staged.Generated[0].VersionID || *variables["DISABLE_REGISTRATION"].Value != "true" {
		t.Fatal("variables", variables)
	}
	b := s.Bindings[0]
	if b.SourceID != database.ID || b.TargetID != app.ID || b.Output != "URL" || b.Variable != "DATABASE_URL" || len(app.DependsOn) != 1 || app.DependsOn[0] != database.ID {
		t.Fatal("Kaneo is not connected to its database")
	}
	r := s.Routes[0]
	if r.TargetID != app.ID || r.Port != 5173 || r.Domain != "tracker.example.com" || r.TLS != "managed" || len(r.AllowCIDRs) != 1 {
		t.Fatal("route", r)
	}
	plain := route()
	plain.TLS = "off"
	if staged, err = kaneo.Stage(Stage{Name: "Kaneo", Route: plain}, nil); err != nil || *byName(staged.Specification.Applications[0])["KANEO_CLIENT_URL"].Value != "http://tracker.example.com" {
		t.Fatal("plain HTTP origin", err)
	}
	generated := func(app uuid.UUID) (string, error) { return "a" + app.String()[:8] + ".example.net", nil }
	if staged, err = kaneo.Stage(Stage{Name: "Kaneo", Route: &StageRoute{TLS: "managed", AllowCIDRs: []string{"2001:db8::/64"}}}, generated); err != nil || staged.Specification.Routes[0].Domain != "a"+staged.Specification.Applications[0].ID.String()[:8]+".example.net" {
		t.Fatal("generated domain for the new service", err)
	}
	for name, in := range map[string]Stage{
		"no route":           {Name: "Kaneo"},
		"open route":         {Name: "Kaneo", Route: &StageRoute{Domain: "tracker.example.com", TLS: "managed"}},
		"unknown secret":     {Name: "Kaneo", Route: route(), Existing: map[uuid.UUID]uuid.UUID{uuid.New(): existing}},
		"long name":          {Name: strings.Repeat("k", 80), Route: route()},
		"bad domain":         {Name: "Kaneo", Route: &StageRoute{Domain: "tracker", TLS: "managed", AllowCIDRs: []string{"203.0.113.7/32"}}},
		"bad allowlist":      {Name: "Kaneo", Route: &StageRoute{Domain: "tracker.example.com", TLS: "managed", AllowCIDRs: []string{"everyone"}}},
		"passthrough on web": {Name: "Kaneo", Route: &StageRoute{Domain: "tracker.example.com", TLS: "passthrough", AllowCIDRs: []string{"203.0.113.7/32"}}},
	} {
		if _, err := kaneo.Stage(in, nil); err == nil {
			t.Fatal("staged with", name)
		}
	}
	nginx, _ := Find("nginx")
	if _, err := nginx.Stage(Stage{Name: "Nginx", Route: route()}, nil); err == nil {
		t.Fatal("route on an entry without one")
	}
}

func byName(a domain.Application) map[string]domain.Variable {
	out := map[string]domain.Variable{}
	for _, v := range a.Variables {
		out[v.Name] = v
	}
	return out
}
