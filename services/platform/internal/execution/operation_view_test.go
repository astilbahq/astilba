package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
)

// A principal that may read the environment sees an operation as written. Any other sees no free
// text: a note becomes WithheldNote and step details are cleared, while an operation without a note
// still has none.
func TestOperationForWithholdsFreeTextFromNonReaders(t *testing.T) {
	op := func(note string) api.Operation {
		return api.Operation{State: "failed", Kind: "visual", Note: note, Steps: []api.ExecutionStep{{Name: "resolve", State: "done", Detail: "DATABASE_URL=secret"}}}
	}
	planner := store.Principal{Automation: &domain.AutomationGrant{Capabilities: []string{domain.DeploymentPlan}}}
	reader := store.Principal{Automation: &domain.AutomationGrant{Capabilities: []string{domain.EnvironmentRead}}}
	for name, p := range map[string]store.Principal{"member": {}, "reader": reader} {
		if got := operationFor(p, op("compose failed: secret")); got.Note != "compose failed: secret" || got.Steps[0].Detail != "DATABASE_URL=secret" {
			t.Fatalf("a %s lost the diagnostics: %+v", name, got)
		}
	}
	got := operationsFor(planner, []api.Operation{op("compose failed: secret"), op("")})
	if got[0].Note != WithheldNote || got[0].Steps[0].Detail != "" || got[0].Steps[0].Name != "resolve" || got[0].State != "failed" {
		t.Fatalf("a non-reader saw the diagnostics: %+v", got[0])
	}
	if got[1].Note != "" || got[1].Steps[0].Detail != "" {
		t.Fatalf("a non-reader's operation without a note gained one or kept a detail: %+v", got[1])
	}
}
