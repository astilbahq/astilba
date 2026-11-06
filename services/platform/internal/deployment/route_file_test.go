package deployment

import (
	"bytes"
	"encoding/json/v2"
	"strings"
	"testing"
	"text/template"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// The proxy runs each file through text/template with the default delimiters, so the
// output must come back byte for byte, and JSON must still read the original value.
func TestRouteFileOpensNoTemplateActionAndKeepsItsValues(t *testing.T) {
	for _, value := range []string{"{{", "{{{", "{{{{", "{{{{{", "/h{{printf `%c` 65}}", "{{- 1 -}}", `\{{`, `\\{{`, `"{{`, "{{/* c */}}"} {
		config := map[string]string{value: value, "plain": value}
		raw, err := RouteFile(config)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := template.New("file").Parse(string(raw))
		if err != nil {
			t.Fatalf("%q opened a template action: %v", value, err)
		}
		var evaluated bytes.Buffer
		if err = tree.Execute(&evaluated, nil); err != nil || !bytes.Equal(evaluated.Bytes(), raw) {
			t.Fatalf("%q changed when evaluated: %v: %s", value, err, evaluated.String())
		}
		var decoded map[string]string
		if err = json.Unmarshal(raw, &decoded); err != nil || decoded[value] != value || decoded["plain"] != value {
			t.Fatalf("%q did not survive: %v: %v", value, err, decoded)
		}
	}
}

// A changed byte here would report ingress drift on every host once its agent updates.
func TestRouteFileLeavesOtherOutputUnchanged(t *testing.T) {
	config := map[string]any{"a": map[string]any{"b": map[string]string{"c": "{x} }} `y` {"}}}
	want, err := stablejson.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RouteFile(config)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %s, want %s: %v", got, want, err)
	}
}

// Every field that reaches the file refuses a delimiter, except a health path, which
// a host may already hold and which must render inert instead.
func TestRouteConfigAdmitsNoTemplateActionFromAnyField(t *testing.T) {
	const action = "{{x}}"
	unit, env, app, peer := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	base := func() RouteSet {
		return RouteSet{UnitID: unit, EnvironmentID: env, Routes: []domain.Route{{
			ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{"10.0.0.0/8"},
			Advanced: &domain.RouteOptions{ResponseHeaders: map[string]string{"X-Example": "value"}, StripPrefix: "/app"},
		}}, Backends: map[string]Backend{app.String(): {InstanceIDs: []uuid.UUID{app, peer}, Health: &domain.HealthCheck{Port: 8080, Path: "/ready"}}}}
	}
	endpoints := []PrivateEndpoint{{InstanceID: app, ServerID: uuid.New(), UnitID: unit, ResourceID: app, Address: ServiceName(app), Port: 8080}}
	refused := map[string]func(*RouteSet){
		"domain":        func(s *RouteSet) { s.Routes[0].Domain = action },
		"allowed range": func(s *RouteSet) { s.Routes[0].AllowCIDRs = []string{action} },
		"header name": func(s *RouteSet) {
			s.Routes[0].Advanced.ResponseHeaders = map[string]string{action: "value"}
		},
		"header value": func(s *RouteSet) {
			s.Routes[0].Advanced.ResponseHeaders = map[string]string{"X-Example": action}
		},
		"strip prefix":    func(s *RouteSet) { s.Routes[0].Advanced.StripPrefix = "/" + action },
		"backend address": func(s *RouteSet) { s.Backends[app.String()] = Backend{Address: action} },
		"endpoint address": func(s *RouteSet) {
			s.Backends[app.String()] = Backend{Endpoints: []PrivateEndpoint{{InstanceID: app, ServerID: uuid.New(), UnitID: unit, ResourceID: app, Address: action, Port: 8080}}}
		},
	}
	for name, change := range refused {
		set := base()
		change(&set)
		if raw, err := RouteConfig(map[string]RouteSet{unit.String(): set}); err == nil {
			t.Errorf("%s reached the file: %s", name, raw)
		}
	}
	health := domain.HealthCheck{Port: 8080, Path: "/" + action}
	for name, backend := range map[string]Backend{
		"replica pool": {InstanceIDs: []uuid.UUID{app, peer}, Health: &health},
		"cross-host":   {Endpoints: endpoints, Health: &health},
	} {
		set := base()
		set.Backends[app.String()] = backend
		raw, err := RouteConfig(map[string]RouteSet{unit.String(): set})
		if err != nil {
			t.Fatalf("%s: a stored health path must still render: %v", name, err)
		}
		if strings.Contains(string(raw), "{{") {
			t.Errorf("%s opened a template action: %s", name, raw)
		}
	}
}

// Recovery, cleanup and backups revalidate frozen plans, and an offline bundle cannot be
// edited, so a path that was admitted before such paths were refused must keep validating.
func TestFrozenPlanWithATemplateActionInAHealthPathStillValidates(t *testing.T) {
	p := testPlan()
	p.Specification.Applications[1].Health.Path = "/{{x}}"
	if err := p.WithHash().Validate(false); err != nil {
		t.Fatal(err)
	}
}
