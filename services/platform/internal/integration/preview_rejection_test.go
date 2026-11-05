package integration

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// A saved policy can fail a stricter check than the one it passed, and a reconcile that
// returns the rejection rolls back its reason and retries the provider for days.
func TestPreviewBuildRejectionSettlesTheRequest(t *testing.T) {
	h := newHungPreview(t)
	const canary = "preview-pasted-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary + "\n"}, 201, &secret)
	spec := h.spec
	spec.Variables = append(append([]domain.Variable{}, spec.Variables...), domain.Variable{Name: "TOKEN", SecretVersionID: &secret.Id})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	if _, err := h.f.st.Pool.Exec(t.Context(), "UPDATE preview_policies SET configuration=jsonb_set(configuration,'{build,arguments}',jsonb_build_object('TOKEN',$1::text)) WHERE workspace_id=$2", canary, h.ws); err != nil {
		t.Fatal(err)
	}
	preview := h.waitPreview(t, "approval_required")
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	preview = h.waitPreview(t, "failed")
	if !strings.Contains(preview.Note, "known secret") || strings.Contains(preview.Note, canary) {
		t.Fatal("the rejection did not reach the preview, or echoed the value", preview.Note)
	}
	if preview.EnvironmentId != nil {
		t.Fatal("a request that can never build kept an environment and its resource slot")
	}
}
