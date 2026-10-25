package deployment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestNamedOutputsResolveRuntimeIdentityAndSecretProvenance(t *testing.T) {
	p := testPlan()
	source := &p.Specification.Applications[1]
	secret, instance, receiver := uuid.New(), uuid.New(), uuid.New()
	source.Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret}}
	source.Outputs = []domain.ServiceOutput{{Name: "PORT", Type: "port", Port: 8080}, {Name: "URL", Type: "url", Port: 8080, Scheme: "http", Path: "/$VALUE"}, {Name: "AUTH", Type: "variable", Variable: "TOKEN"}}
	for _, name := range []string{"HOST", "PORT", "URL", "AUTH"} {
		p.Specification.Bindings = append(p.Specification.Bindings, domain.Binding{ID: uuid.New(), SourceID: source.ID, TargetID: receiver, Variable: name, Output: name})
	}
	sourceID := source.ID
	p.Instances = map[string]uuid.UUID{sourceID.String(): instance}
	p.Specification.Applications = append(p.Specification.Applications, domain.Application{ID: receiver, Name: "Consumer", Kind: "worker", Source: source.Source})
	p.Images[receiver.String()] = *source.Source.Image
	p = p.WithHash()
	values := map[string]string{secret.String(): "token/$value", p.Specification.Applications[0].Postgres.PasswordSecretVersionID.String(): "password"}
	raw, err := Compose(p, values, false)
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Services map[string]struct {
			Environment map[string]string
			DependsOn   map[string]any `json:"depends_on"`
		}
	}
	if err = json.Unmarshal(raw, &rendered); err != nil {
		t.Fatal(err)
	}
	env := rendered.Services[ServiceName(receiver)].Environment
	if env["HOST"] != ServiceName(instance) || env["PORT"] != "8080" || env["URL"] != "http://"+ServiceName(instance)+":8080/$$VALUE" || env["AUTH"] != "token/$$value" || len(rendered.Services[ServiceName(receiver)].DependsOn) != 0 {
		t.Fatalf("wrong output resolution: %+v", rendered)
	}
	if len(domain.SecretReferences(p.Specification)) != 2 {
		t.Fatal("output lost source secret provenance")
	}
	delete(values, secret.String())
	if _, err = Compose(p, values, false); err == nil {
		t.Fatal("missing secret silently substituted")
	}
	exported, err := Compose(p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), "token/") || !strings.Contains(string(exported), "ASTILBA_SECRET_"+strings.ReplaceAll(secret.String(), "-", "")) || !strings.Contains(string(exported), "/$$VALUE") {
		t.Fatal("export lost placeholder or literal dollar")
	}
	changed := copyPlan(p)
	changed.Specification.Applications[1].Outputs[0].Port++
	if changed.WithHash().Hash == p.Hash {
		t.Fatal("output change did not invalidate reviewed plan")
	}
}

func TestGeneralReferencesAreNotReadinessEdges(t *testing.T) {
	p := testPlan()
	a := p.Specification.Applications[1]
	b := a
	b.ID = uuid.New()
	b.Name = "Peer"
	p.Specification.Applications = []domain.Application{a, b}
	p.Specification.Bindings = []domain.Binding{{ID: uuid.New(), SourceID: a.ID, TargetID: b.ID, Output: "HOST", Variable: "PEER"}, {ID: uuid.New(), SourceID: b.ID, TargetID: a.ID, Output: "HOST", Variable: "PEER"}}
	parse := func() error { raw, _ := json.Marshal(p.Specification); _, _, err := domain.ParseSpec(raw); return err }
	if err := parse(); err != nil {
		t.Fatal("reciprocal static references rejected", err)
	}
	p.Specification.Applications[0].DependsOn = []uuid.UUID{b.ID}
	if err := parse(); err != nil {
		t.Fatal("one-way explicit readiness became cyclic", err)
	}
	p.Specification.Applications[1].DependsOn = []uuid.UUID{a.ID}
	if parse() == nil {
		t.Fatal("actual readiness cycle accepted")
	}
}

func TestOutputAdmissionRejectsUnresolvableOrConflictingReferences(t *testing.T) {
	for name, change := range map[string]func(*domain.Specification){
		"undeclared variable": func(s *domain.Specification) {
			s.Applications[1].Outputs = []domain.ServiceOutput{{Name: "TOKEN", Type: "variable", Variable: "NOT_DECLARED"}}
		},
		"binding variable is not source input": func(s *domain.Specification) {
			s.Applications[1].Outputs = []domain.ServiceOutput{{Name: "URL", Type: "variable", Variable: "DATABASE_URL"}}
		},
		"reserved output": func(s *domain.Specification) {
			s.Applications[1].Outputs = []domain.ServiceOutput{{Name: "HOST", Type: "port", Port: 80}}
		},
		"unavailable output": func(s *domain.Specification) { s.Bindings[0].Output = "UNKNOWN" },
		"variable collision": func(s *domain.Specification) {
			s.Variables = []domain.Variable{{Name: "DATABASE_URL", Value: ptrString("literal")}}
		},
		"ambiguous output": func(s *domain.Specification) {
			s.Applications[1].Outputs = []domain.ServiceOutput{{Name: "URL", Type: "url", Scheme: "http", Port: 80, Variable: "OTHER"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := testPlan()
			change(&p.Specification)
			raw, _ := json.Marshal(p.Specification)
			if _, _, err := domain.ParseSpec(raw); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}
