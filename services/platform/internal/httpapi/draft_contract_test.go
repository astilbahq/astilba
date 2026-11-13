package httpapi

import (
	"maps"
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/getkin/kin-openapi/openapi3"
)

// A draft save sends a Specification whose Compose bundle may leave out the namespace and origin
// the draft keeps. The contract repeats both schemas for that, so they must not drift apart.
func TestDraftSpecificationIsTheSpecificationASaveSends(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	schema := func(name string) *openapi3.Schema {
		t.Helper()
		ref, ok := spec.Components.Schemas[name]
		if !ok || ref.Value == nil {
			t.Fatalf("the contract has no %s", name)
		}
		return ref.Value
	}
	same := func(name, draft string, differ map[string]string, required []string) {
		t.Helper()
		shared, sent := schema(name), schema(draft)
		if !slices.Equal(slices.Sorted(maps.Keys(shared.Properties)), slices.Sorted(maps.Keys(sent.Properties))) {
			t.Fatalf("%s and %s have different properties", name, draft)
		}
		for property, ref := range shared.Properties {
			if differ[property] != "" {
				if sent.Properties[property].Ref != differ[property] {
					t.Fatalf("%s.%s doesn't refer to %s", draft, property, differ[property])
				}
				continue
			}
			a, _ := ref.MarshalJSON()
			b, _ := sent.Properties[property].MarshalJSON()
			if string(a) != string(b) {
				t.Fatalf("%s.%s differs from %s.%s", draft, property, name, property)
			}
		}
		if !slices.Equal(slices.Sorted(slices.Values(sent.Required)), required) {
			t.Fatalf("%s requires %v", draft, sent.Required)
		}
	}
	same("Specification", "DraftSpecification", map[string]string{"compose": "#/components/schemas/DraftComposeBundle"}, slices.Sorted(slices.Values(schema("Specification").Required)))
	same("ComposeBundle", "DraftComposeBundle", nil, slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(schema("ComposeBundle").Required), func(name string) bool { return name == "namespace" }))))
	if !slices.Contains(schema("ComposeBundle").Required, "namespace") {
		t.Fatal("a stored Compose bundle no longer requires its namespace")
	}
}
