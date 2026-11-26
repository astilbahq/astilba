package httpapi

import (
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/getkin/kin-openapi/openapi3"
)

// An operation's note and step details can quote the environment's configuration, so execution's
// operationFor withholds them from a credential that may not read it. This lists the API operations
// such a credential may call that answer with an operation, so that allowing a new one means
// passing it through operationFor and adding it here.
func TestOperationsShownWithoutEnvironmentReadAreWithheld(t *testing.T) {
	withheld := []string{"ApplyVisualPlan", "GetEnvironmentOperation", "RunConfiguredBuild"}
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]*openapi3.Operation{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			operations[op.OperationID] = op
		}
	}
	answering := []string{}
	for name, capabilities := range automationOperations {
		if slices.Equal(capabilities, []string{domain.EnvironmentRead}) {
			continue
		}
		op, ok := operations[name]
		if !ok {
			t.Fatalf("the contract has no %s", name)
		}
		seen := map[*openapi3.Schema]bool{}
		for status, response := range op.Responses.Map() {
			if !strings.HasPrefix(status, "2") || response.Value == nil {
				continue
			}
			for _, media := range response.Value.Content {
				if carriesOperation(media.Schema, seen) && !slices.Contains(answering, name) {
					answering = append(answering, name)
				}
			}
		}
	}
	slices.Sort(answering)
	if !slices.Equal(answering, withheld) {
		t.Fatalf("operations a credential without environment:read may call that answer with an operation are %v, not %v: pass each through operationFor", answering, withheld)
	}
}

func carriesOperation(ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) bool {
	if ref == nil {
		return false
	}
	if ref.Ref == "#/components/schemas/Operation" {
		return true
	}
	s := ref.Value
	if s == nil || seen[s] {
		return false
	}
	seen[s] = true
	refs := []*openapi3.SchemaRef{s.Items, s.Not, s.AdditionalProperties.Schema}
	for _, group := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
		refs = append(refs, group...)
	}
	for _, property := range s.Properties {
		refs = append(refs, property)
	}
	return slices.ContainsFunc(refs, func(r *openapi3.SchemaRef) bool { return carriesOperation(r, seen) })
}

// A credential reaches only operations that declare automationBearer, so every operation it is
// allowed must declare it, and none that takes a browser session or nothing may.
func TestBearerOperationsFollowTheContract(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	reachable := bearerOperations(spec)
	byName := map[string]string{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			byName[op.OperationID] = method + " " + path
		}
	}
	for name := range automationOperations {
		if !reachable[byName[name]] {
			t.Errorf("%s is allowed to a credential but does not declare automationBearer", name)
		}
	}
	for _, name := range []string{"GetAutomationContext"} {
		if !reachable[byName[name]] {
			t.Errorf("%s does not declare automationBearer", name)
		}
	}
	for _, name := range []string{"Logout", "Login", "Session", "VerifyMFA", "OpenTroubleshootingSession", "CloseTroubleshootingSession"} {
		if reachable[byName[name]] {
			t.Errorf("%s takes a browser session or nothing, but a credential reaches it", name)
		}
	}
}
