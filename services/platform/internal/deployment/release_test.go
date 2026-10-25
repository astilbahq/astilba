package deployment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func copyPlan(p Plan) Plan {
	raw, _ := json.Marshal(p)
	var n Plan
	_ = json.Unmarshal(raw, &n)
	return n
}
func TestProtectedReleaseEligibilityUsesEffectiveImages(t *testing.T) {
	old := testPlan()
	web := old.Specification.Applications[1].ID
	old.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: web, Protocol: "http", Domain: "release.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	next := copyPlan(old)
	next.ID = uuid.New()
	next.Images[web.String()] = "busybox@sha256:" + strings.Repeat("a", 64)
	if app, err := ReleaseApplication(old, next); err != nil || app.ID != web {
		t.Fatalf("moved mutable tag should be protected: %v", err)
	}
	consumer := domain.Application{ID: uuid.New(), Name: "Consumer", Kind: "worker", Source: old.Specification.Applications[1].Source}
	boundOld, boundNext := copyPlan(old), copyPlan(next)
	for _, plan := range []*Plan{&boundOld, &boundNext} {
		plan.Specification.Applications = append(plan.Specification.Applications, consumer)
		plan.Images[consumer.ID.String()] = plan.Images[web.String()]
		plan.Specification.Bindings = append(plan.Specification.Bindings, domain.Binding{ID: uuid.New(), SourceID: web, TargetID: consumer.ID, Output: "HOST", Variable: "HOST"})
	}
	boundNext.Specification.Bindings = boundOld.Specification.Bindings
	boundNext.Images[consumer.ID.String()] = boundOld.Images[consumer.ID.String()]
	if _, err := ReleaseApplication(boundOld, boundNext); err == nil {
		t.Fatal("protected release invalidated a consumer's private address")
	}
	next.Images[old.Specification.Applications[0].ID.String()] = "postgres@sha256:" + strings.Repeat("b", 64)
	if _, err := ReleaseApplication(old, next); err == nil {
		t.Fatal("two effective image changes entered one-app executor")
	}
	cases := map[string]func(*Plan){
		"global configuration": func(p *Plan) {
			p.Specification.Variables = []domain.Variable{{Name: "MODE", Value: ptrString("different")}}
		},
		"mounted application": func(p *Plan) {
			p.Specification.Applications[1].Mounts = []domain.VolumeMount{{VolumeID: p.Specification.Volumes[0].ID, Target: "/data"}}
		},
		"dependent":      func(p *Plan) { p.Specification.Applications[0].DependsOn = []uuid.UUID{web} },
		"unverified":     func(p *Plan) { p.Specification.Applications[1].Health = nil },
		"fixed listener": func(p *Plan) { p.Specification.Routes[0].Protocol = "tcp" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			n := copyPlan(old)
			n.Images[web.String()] = "busybox@sha256:" + strings.Repeat("a", 64)
			change(&n)
			if _, err := ReleaseApplication(old, n); err == nil {
				t.Fatal("ineligible change accepted")
			}
		})
	}
}
func ptrString(v string) *string { return &v }
func TestReleaseInstanceRenderingKeepsLogicalOwnership(t *testing.T) {
	p := testPlan()
	app := p.Specification.Applications[1]
	instance := uuid.New()
	p.Instances = map[string]uuid.UUID{app.ID.String(): instance}
	p = p.WithHash()
	secret := p.Specification.Applications[0].Postgres.PasswordSecretVersionID
	raw, err := Compose(p, map[string]string{secret.String(): "example-password"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			ContainerName string `json:"container_name"`
			Labels        map[string]string
			Networks      map[string]struct{ Aliases []string }
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc.Services[ServiceName(app.ID)]; exists {
		t.Fatal("candidate inherited the serving revision DNS service key")
	}
	service := doc.Services[p.HostName(app.ID)]
	if service.ContainerName != UnitName(instance) || service.Labels["com.astilba.resource"] != app.ID.String() || service.Networks["unit"].Aliases[0] != ServiceName(instance) {
		t.Fatal("runtime isolation lost logical identity")
	}
	set := RouteSet{UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: []domain.Route{{ID: uuid.New(), TargetID: app.ID, Protocol: "http", Domain: "release.example.test", Port: 8080, TLS: "off"}}, Backends: map[string]Backend{app.ID.String(): p.Backend(app.ID)}}
	if _, err = RouteConfig(map[string]RouteSet{p.UnitID.String(): set}); err != nil {
		t.Fatal(err)
	}
	p.Instances[app.ID.String()] = p.Specification.Applications[0].ID
	p = p.WithHash()
	if p.Validate(true) == nil {
		t.Fatal("candidate collided with an existing logical container")
	}
}
