package domain

import (
	"testing"

	"github.com/google/uuid"
)

// Only "{{" opens a template action, so other braces stay valid. A path the applied
// specification holds stays valid too, because some applications cannot differ from it.
func TestValidateNewDraftRefusesANewTemplateActionInAHealthPath(t *testing.T) {
	app, other := uuid.New(), uuid.New()
	spec := func(id uuid.UUID, path string) Specification {
		return Specification{Applications: []Application{{}, {ID: id, Health: &HealthCheck{Port: 8080, Path: path}}}}
	}
	for _, c := range []struct {
		next    string
		applied Specification
		refused bool
	}{
		{next: "/"},
		{next: "/ready?a=b"},
		{next: "/{id}"},
		{next: "/{ {"},
		{next: "/a}}"},
		{next: "/{{", refused: true},
		{next: "/a{{b}}c", refused: true},
		{next: "/{{{", refused: true},
		{next: "/{{- printf -}}", refused: true},
		{next: "/{{x}}", applied: spec(app, "/{{x}}")},
		{next: "/{{y}}", applied: spec(app, "/{{x}}"), refused: true},
		{next: "/{{x}}", applied: spec(other, "/{{x}}"), refused: true},
	} {
		if err := ValidateNewDraft(spec(app, c.next), c.applied); (err != nil) != c.refused {
			t.Errorf("%q over %+v: %v", c.next, c.applied, err)
		}
	}
}
