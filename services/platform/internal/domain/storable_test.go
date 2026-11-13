package domain

import (
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// PostgreSQL refuses NUL in jsonb, and the refused cast aborts the saving transaction, so a
// specification holding one must be refused as invalid on the field that holds it.
func TestParseSpecRefusesNULWhereverItIs(t *testing.T) {
	nul := string(rune(0))
	base := func() Specification {
		image, provider, repository, ref := "busybox:1.37.0", "gitea", "https://git.example.test/team/app.git", "main"
		return Specification{Version: 1, Variables: []Variable{}, Applications: []Application{
			{ID: uuid.New(), Name: "web", Source: ApplicationSource{Kind: "image", Image: &image}},
			{ID: uuid.New(), Name: "api", Source: ApplicationSource{Kind: "git", Provider: &provider, Repository: &repository, Ref: &ref}},
		}}
	}
	parse := func(spec Specification) error {
		raw, err := stablejson.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = ParseSpec(raw)
		return err
	}
	if err := parse(base()); err != nil {
		t.Fatal("the specification without NUL was refused", err)
	}
	for _, c := range []struct {
		field string
		edit  func(*Specification)
	}{
		{"specification.applications.source.image", func(s *Specification) { *s.Applications[0].Source.Image += nul }},
		{"specification.applications.source.ref", func(s *Specification) { *s.Applications[1].Source.Ref += nul }},
		{"specification.applications.source.repository", func(s *Specification) { *s.Applications[1].Source.Repository += nul }},
		{"specification.applications.name", func(s *Specification) { s.Applications[1].Name += nul }},
		{"specification.applications.command", func(s *Specification) { s.Applications[0].Command = []string{"sh", nul} }},
		{"specification.variables.value", func(s *Specification) {
			value := "a" + nul
			s.Variables = []Variable{{Name: "A", Value: &value}}
		}},
		// A map's keys are content, so a key is named by its map rather than quoted.
		{"specification.routes.advanced.responseHeaders", func(s *Specification) {
			s.Routes = []Route{{ID: uuid.New(), TargetID: s.Applications[0].ID, Protocol: "http", Domain: "web.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}, Advanced: &RouteOptions{ResponseHeaders: map[string]string{"X-A" + nul: "b"}}}}
		}},
		{"specification.routes.advanced.responseHeaders", func(s *Specification) {
			s.Routes = []Route{{ID: uuid.New(), TargetID: s.Applications[0].ID, Protocol: "http", Domain: "web.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}, Advanced: &RouteOptions{ResponseHeaders: map[string]string{"X-A": nul}}}}
		}},
	} {
		spec := base()
		c.edit(&spec)
		err := parse(spec)
		refused, ok := err.(*Error)
		if !ok || refused.Code != "invalid_input" || !slices.Equal(refused.Fields, []string{c.field}) {
			t.Errorf("NUL in %s was answered with %v", c.field, err)
		}
	}
}
