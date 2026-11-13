package domain

import (
	"encoding/json/jsontext"
	"reflect"
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

type nulProbe struct {
	Name    string                    `json:"name"`
	Labels  map[string]string         `json:"labels"`
	Steps   []nulStep                 `json:"steps"`
	Raw     *jsontext.Value           `json:"raw"`
	Any     any                       `json:"any"`
	Nested  map[string]map[string]any `json:"nested"`
	Ignored string                    `json:"-"`
	hidden  string
	nulEmbedded
}

type nulStep struct {
	Detail *string `json:"detail"`
}

type nulEmbedded struct {
	Inline string `json:"inline"`
}

// The walk names the member a client sent, so an API refusal can point at it.
func TestNULFieldNamesTheMember(t *testing.T) {
	nul := string(rune(0))
	detail := "ok" + nul
	escaped := jsontext.Value(`{"a":[{"b":"` + `\` + `u0000"}]}`)
	escapedKey := jsontext.Value(`{"a":{"b` + `\` + `u0000":1}}`)
	for want, probe := range map[string]nulProbe{
		"name":         {Name: nul},
		"labels":       {Labels: map[string]string{"a" + nul: "b"}},
		"steps.detail": {Steps: []nulStep{{}, {Detail: &detail}}},
		"raw.a.b":      {Raw: &escaped},
		"raw.a":        {Raw: &escapedKey},
		"any":          {Any: map[string]any{"a": []any{nul}}},
		"nested":       {Nested: map[string]map[string]any{"a": {"b": nul}}},
		"inline":       {nulEmbedded: nulEmbedded{Inline: nul}},
	} {
		if got, found := NULField(probe, ""); !found || got != want {
			t.Errorf("NUL in %s was named %q, %v", want, got, found)
		}
	}
	quoted := jsontext.Value(`{"a":"\\` + `u0000"}`)
	for name, probe := range map[string]nulProbe{
		"none":            {Name: "a", Labels: map[string]string{"a": "b"}, Any: []any{"a", 1.0}},
		"an escaped \\":   {Raw: &quoted},
		"a skipped field": {Ignored: nul, hidden: nul},
	} {
		if field, found := NULField(probe, ""); found {
			t.Errorf("%s: found NUL in %s", name, field)
		}
	}
	if field, found := NULField(nul, ""); !found || field != "request" {
		t.Errorf("a NUL body was named %q", field)
	}
}

// What a host reported keeps its shape and size, with a space for each NUL, wherever it is.
func TestReplaceNULKeepsWhatAHostReported(t *testing.T) {
	nul := string(rune(0))
	detail := "a" + nul + "b"
	raw := jsontext.Value(`{"a":"x` + `\` + `u0000y","b":"\\` + `u0000"}`)
	probe := nulProbe{
		Name:    nul,
		Labels:  map[string]string{"k" + nul: "v" + nul, "plain": "kept"},
		Steps:   []nulStep{{Detail: &detail}},
		Raw:     &raw,
		Any:     map[string]any{"a": []any{nul, 1.0}},
		Nested:  map[string]map[string]any{"a": {"b" + nul: nul}},
		Ignored: nul,
	}
	probe.Inline = nul
	if field, collides := ReplaceNUL(&probe); collides {
		t.Fatal("replacing collided in", field)
	}
	want := nulProbe{
		Name:    " ",
		Labels:  map[string]string{"k ": "v ", "plain": "kept"},
		Raw:     probe.Raw,
		Any:     map[string]any{"a": []any{" ", 1.0}},
		Nested:  map[string]map[string]any{"a": {"b ": " "}},
		Ignored: nul,
	}
	want.Inline = " "
	if *probe.Steps[0].Detail != "a b" || &detail != probe.Steps[0].Detail {
		t.Error("the step's detail was", *probe.Steps[0].Detail)
	}
	probe.Steps = nil
	if !reflect.DeepEqual(probe, want) {
		t.Errorf("replaced into %+v", probe)
	}
	if string(raw) != `{"a":"x`+`\`+`u0020y","b":"\\`+`u0000"}` {
		t.Errorf("the raw value became %s", raw)
	}
	if _, found := NULField(probe, ""); found {
		t.Error("NUL survived the replacement")
	}
}

// Two keys that a replacement would make equal cannot both be kept, so the body is refused.
func TestReplaceNULRefusesKeysItWouldMerge(t *testing.T) {
	nul := string(rune(0))
	probe := nulProbe{Labels: map[string]string{"a" + nul: "1", "a ": "2"}}
	if field, collides := ReplaceNUL(&probe); !collides || field != "labels" {
		t.Fatalf("answered %q, %v", field, collides)
	}
	probe = nulProbe{Labels: map[string]string{"a" + nul + " ": "1", "a " + nul: "2"}}
	if field, collides := ReplaceNUL(&probe); !collides || field != "labels" {
		t.Fatalf("answered %q, %v", field, collides)
	}
	probe = nulProbe{Any: map[string]any{"a" + nul: 1.0, "a ": 2.0}}
	if field, collides := ReplaceNUL(&probe); !collides || field != "any" {
		t.Fatalf("inside an interface, answered %q, %v", field, collides)
	}
}
