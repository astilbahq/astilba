package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// A checkpoint's approval is restored only into the workspace and root being
// recovered, whatever identifiers its JSON carries.
func TestDecodeRecoveryApprovalPinsWorkspaceAndRoot(t *testing.T) {
	ws, parent, key := uuid.New(), uuid.New(), uuid.New()
	approval := func(workspace, root, request uuid.UUID) portability.Row {
		return appliedTestRow(map[string]any{"workspace_id": workspace, "request_key": request, "parent_id": root, "upgrade_id": uuid.New(), "action": "cutover", "expected_phase": "ready"})
	}
	if a, raw, err := decodeRecoveryApproval(ws, parent, approval(ws, parent, key)); err != nil || a.RequestKey != key || a.Action != "cutover" || len(raw) == 0 {
		t.Fatalf("matching approval: %+v %v", a, err)
	}
	for name, row := range map[string]portability.Row{
		"foreign workspace": approval(uuid.New(), parent, key),
		"foreign root":      approval(ws, uuid.New(), key),
		"no request key":    approval(ws, parent, uuid.Nil),
		"missing identity":  {},
	} {
		if _, _, err := decodeRecoveryApproval(ws, parent, row); err == nil {
			t.Errorf("%s: approval accepted", name)
		}
	}
}
