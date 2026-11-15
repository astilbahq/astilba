package portability

import (
	"encoding/json/jsontext"
	"maps"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// A cleanup acknowledged by a report and the same cleanup fenced before its report are one
// effect, in either order; two different effects conflict.
func TestRuntimeOutcomesAcceptEquivalentRemovalAcknowledgements(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	c := api.ExecutionCommand{Id: id, ServerId: uuid.New(), Kind: "visual", Generation: 4}
	row := Row{}
	for key, value := range map[string]any{"id": id, "workspace_id": ws, "server_id": c.ServerId, "environment_id": nil, "generation": 4, "plan_id": nil, "request_key": uuid.New(), "kind": "visual", "parent_id": nil, "command": c, "state": "succeeded", "report_sequence": 1, "cancel_requested": false, "runtime_outcome": "removed", "runtime_plan_id": nil} {
		setValue(row, key, value)
	}
	transferred := maps.Clone(row)
	setValue(transferred, "state", "cancelled")
	setValue(transferred, "report_sequence", 0)
	setValue(transferred, "runtime_outcome", nil)
	for _, input := range [][]Row{{row, transferred}, {transferred, row}} {
		outcome, equivalent, err := MergeRuntimeOutcomes(input[0], input[1])
		if err != nil || !equivalent || outcome.Kind != "removed" {
			t.Fatal("equivalent cleanup acknowledgement rejected or erased", outcome, equivalent, err)
		}
	}
	conflicting := maps.Clone(transferred)
	setValue(conflicting, "runtime_outcome", "applied")
	setValue(conflicting, "runtime_plan_id", uuid.New())
	if _, _, err := MergeRuntimeOutcomes(row, conflicting); err == nil {
		t.Fatal("conflicting actual effects accepted")
	}
}

// Recovery bundles written by an earlier release are compared against these hashes.
func TestRecoveryHashesKeepTheirReleasedValues(t *testing.T) {
	if got := recoveryRowHash([]byte(`{ "z": 12345678901234567890, "a": "<&>", "f": 1.50 }`)); got != "5c49f6125abc1c236566d3a168d97c59e8e09a18058fa283efc2b59c865a8f83" {
		t.Errorf("row hash changed to %s", got)
	}
	if got := recoveryRowHash([]byte(`not json`)); got != "7ccfa1fbf3940e6f0c0375d87c0f9235a50514e14cb427bdfaf5077987b26ccf" {
		t.Errorf("unparseable row hash changed to %s", got)
	}
	state, err := recoveryStateHash(Archive{Name: "a && b", RequireMFA: true, Tables: map[string][]Row{"z": {{"k": jsontext.Value(`{ "x" : "<" }`), "n": jsontext.Value("null")}}, "a": nil}})
	if err != nil || state != "1984a913f487793d106b3381b1780af5355345ee1e56fa420428c7fb472ab0cd" {
		t.Errorf("state hash changed to %s, %v", state, err)
	}
}
