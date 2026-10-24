package deployment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func testPlan() Plan {
	image := domain.PostgresImage
	db, web, volume, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	appImage := "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	return Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: Recipe, ExpiresAt: time.Now().Add(time.Minute), Images: map[string]string{db.String(): image, web.String(): appImage}, Specification: domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Applications: []domain.Application{{ID: db, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/var/lib/postgresql"}}}, {ID: web, Name: "Web", Kind: "web", Source: domain.ApplicationSource{Kind: "image", Image: &appImage}, Command: []string{"sh", "-c", "echo $VALUE"}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: db, TargetID: web, Variable: "DATABASE_URL"}}}}.WithHash()
}

func TestBindingRenderingAndPortableSecrets(t *testing.T) {
	p := testPlan()
	secret := p.Specification.Applications[0].Postgres.PasswordSecretVersionID
	plain := "a/$b@c: d"
	raw, err := Compose(p, map[string]string{secret.String(): plain}, false)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Environment map[string]string
			Command     []string
			DependsOn   map[string]any `json:"depends_on"`
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	web := doc.Services[ServiceName(p.Specification.Applications[1].ID)]
	if !strings.Contains(web.Environment["DATABASE_URL"], "a%2F$$b%40c%3A%20d") || len(web.DependsOn) != 1 || web.Command[2] != "echo $$VALUE" {
		t.Fatalf("binding, readiness or literal command changed: %+v", web)
	}
	exported, err := Compose(p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), plain) || !strings.Contains(string(exported), "_URLENCODED:?") || !strings.Contains(string(exported), "\"external\": true") {
		t.Fatal("export lost placeholders or retained-storage mapping")
	}
	// A reviewed digest cannot be substituted with another repository or a mutable tag.
	p.Images[p.Specification.Applications[1].ID.String()] = "attacker/image@sha256:" + strings.Repeat("a", 64)
	p = p.WithHash()
	if p.Validate(true) == nil {
		t.Fatal("foreign image accepted")
	}
}

func TestVisualContractRejections(t *testing.T) {
	cases := map[string]func(*domain.Specification){
		"host mount": func(s *domain.Specification) { s.Applications[0].Mounts[0].Target = "/var/run/docker.sock" },
		"wrong binding type": func(s *domain.Specification) {
			s.Bindings[0].SourceID = s.Applications[1].ID
			s.Bindings[0].TargetID = s.Applications[0].ID
		},
		"foreign volume":                  func(s *domain.Specification) { s.Applications[0].Mounts[0].VolumeID = uuid.New() },
		"readiness cycle through binding": func(s *domain.Specification) { s.Applications[0].DependsOn = []uuid.UUID{s.Applications[1].ID} },
		"shared database storage": func(s *domain.Specification) {
			a := s.Applications[0]
			a.ID = uuid.New()
			a.Name = "Other database"
			s.Applications = append(s.Applications, a)
		},
		"duplicate domain": func(s *domain.Specification) {
			r := domain.Route{ID: uuid.New(), TargetID: s.Applications[1].ID, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off"}
			s.Routes = []domain.Route{r, r}
			s.Routes[1].ID = uuid.New()
		},
		"public TCP without access policy": func(s *domain.Specification) {
			s.Routes = []domain.Route{{ID: uuid.New(), TargetID: s.Applications[0].ID, Protocol: "tcp", Domain: "db.example.test", Port: 5432, ListenPort: 15432, TLS: "off"}}
		},
		"provider template": func(s *domain.Specification) {
			s.Routes = []domain.Route{{ID: uuid.New(), TargetID: s.Applications[1].ID, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", Advanced: &domain.RouteOptions{ResponseHeaders: map[string]string{"X-Test": "{{env SECRET}}"}}}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := testPlan()
			change(&p.Specification)
			raw, _ := json.Marshal(p.Specification)
			if _, _, err := domain.ParseSpec(raw); err == nil {
				t.Fatal("invalid visual specification accepted")
			}
		})
	}
}

func TestRouteRegistryNamespacesAndValidation(t *testing.T) {
	p := testPlan()
	app := p.Specification.Applications[1].ID
	r := domain.Route{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", Advanced: &domain.RouteOptions{StripPrefix: "/app", RequestsPerSecond: 20}}
	set := RouteSet{EnvironmentID: p.EnvironmentID, UnitID: p.UnitID, Routes: []domain.Route{r}, Backends: map[string]Backend{app.String(): {Address: ServiceName(app)}}}
	all := map[string]RouteSet{p.UnitID.String(): set}
	raw, err := RouteConfig(all)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), UnitName(p.UnitID)+"-"+r.ID.String()+"-prefix") {
		t.Fatal("middleware is not namespaced")
	}
	set.Backends[app.String()] = Backend{Address: "api@internal"}
	if _, err = RouteConfig(all); err == nil {
		t.Fatal("foreign backend accepted")
	}
	set.Backends[app.String()] = Backend{Address: ServiceName(app)}
	set.Routes[0].Domain = "evil`) || PathPrefix(`/"
	all[p.UnitID.String()] = set
	if _, err = RouteConfig(all); err == nil {
		t.Fatal("stored registry bypassed route validation")
	}
}

func TestStorageSwitchIsNamedInReviewedEffects(t *testing.T) {
	app, old, newID := uuid.New(), uuid.New(), uuid.New()
	previous := domain.Specification{Volumes: []domain.Volume{{ID: old, Name: "data"}}, Applications: []domain.Application{{ID: app, Name: "database", Mounts: []domain.VolumeMount{{VolumeID: old, Target: "/data"}}}}}
	next := domain.Specification{Volumes: []domain.Volume{{ID: newID, Name: "data2"}}, Applications: []domain.Application{{ID: app, Name: "database", Mounts: []domain.VolumeMount{{VolumeID: newID, Target: "/data"}}}}}
	effects := strings.Join(Effects(next, previous, false), "\n")
	for _, required := range []string{"Remove volume data ", "Attach volume data2 ", "Change database storage at /data from data to data2; data is not copied."} {
		if !strings.Contains(effects, required) {
			t.Fatalf("missing storage warning %q: %s", required, effects)
		}
	}
}
