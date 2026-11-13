package portability

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestRecoveryBasesRetainMissingHistoryAndKnownOutcomes(t *testing.T) {
	ws, parent, history := uuid.New(), uuid.New(), uuid.New()
	command := api.ExecutionCommand{Id: history, ServerId: uuid.New(), Kind: "crosshost", Generation: 3, Deadline: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
	operation := Row{}
	for key, value := range map[string]any{"id": history, "workspace_id": ws, "server_id": command.ServerId, "environment_id": nil, "generation": command.Generation, "plan_id": nil, "request_key": uuid.New(), "kind": command.Kind, "parent_id": nil, "command": command, "state": "running", "report_sequence": 0, "steps": []any{}, "cancel_requested": false} {
		setValue(operation, key, value)
	}
	base := func(at int) Archive {
		return Archive{Version: 5, Workspace: ws, Name: "Recovery", CreatedAt: time.Date(2026, 1, 1, 0, at, 0, 0, time.UTC), Tables: map[string][]Row{}}
	}
	older, newer, middle := base(1), base(3), base(2)
	completed := maps.Clone(operation)
	setValue(completed, "state", "succeeded")
	setValue(completed, "cancel_requested", true)
	older.Tables["execution_operations"] = []Row{completed}
	newer.Tables["execution_operations"] = []Row{operation}
	// The later fork never knew this active parent or immutable revision.
	missing := maps.Clone(operation)
	setValue(missing, "id", parent)
	frozen := Row{}
	setValue(frozen, "id", uuid.New())
	setValue(frozen, "specification", map[string]any{"version": 1})
	older.Tables["execution_operations"] = append(older.Tables["execution_operations"], missing)
	older.Tables["specification_revisions"] = []Row{frozen}
	project := Row{}
	setValue(project, "id", uuid.New())
	setValue(project, "name", "Older")
	older.Tables["projects"] = []Row{project}
	changed := maps.Clone(project)
	setValue(changed, "name", "Middle")
	middle.Tables["projects"] = []Row{changed}
	original, _ := stablejson.Marshal(older)
	combine := func(inputs ...Archive) Archive {
		t.Helper()
		var combined RecoveryBases
		for _, a := range inputs {
			if err := combined.Add(a); err != nil {
				t.Fatal(err)
			}
		}
		return combined.Archive()
	}
	a, b := combine(older, newer, middle), combine(middle, newer, older)
	if !reflect.DeepEqual(a, b) || len(a.Tables["execution_operations"]) != 2 || len(a.Tables["specification_revisions"]) != 1 || string(a.Tables["projects"][0]["name"]) != `"Middle"` {
		t.Fatal("base order lost missing or newer per-row history")
	}
	for _, row := range a.Tables["execution_operations"] {
		var id uuid.UUID
		_ = json.Unmarshal(row["id"], &id)
		if id == history && (string(row["state"]) != `"succeeded"` || string(row["cancel_requested"]) != "true") {
			t.Fatal("a later fork erased known completion or cancellation")
		}
	}
	after, _ := stablejson.Marshal(older)
	if string(original) != string(after) {
		t.Fatal("combining bases changed reusable input")
	}
	for _, name := range []string{"immutable", "same-time metadata", "terminal", "sequence"} {
		t.Run(name, func(t *testing.T) {
			conflicting := base(4)
			switch name {
			case "immutable":
				row := maps.Clone(frozen)
				setValue(row, "specification", map[string]any{"version": 2})
				conflicting.Tables["specification_revisions"] = []Row{row}
			case "same-time metadata":
				conflicting.CreatedAt = older.CreatedAt
				conflicting.Tables["projects"] = []Row{changed}
			case "terminal":
				row := maps.Clone(completed)
				setValue(row, "state", "failed")
				conflicting.Tables["execution_operations"] = []Row{row}
			case "sequence":
				row := maps.Clone(operation)
				setValue(row, "report_sequence", 1)
				conflicting.Tables["execution_operations"] = []Row{row}
			}
			var combined RecoveryBases
			if err := combined.Add(older); err != nil {
				t.Fatal(err)
			}
			if err := combined.Add(conflicting); err == nil {
				t.Fatal("conflicting base knowledge was accepted")
			}
		})
	}
}

func TestRecoveryBasesProgressSurvivesDiagnosticRedactionAndInputOrder(t *testing.T) {
	ws, id, server, key := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	command := api.ExecutionCommand{Id: id, ServerId: server, Kind: "crosshost", Generation: 3, Deadline: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
	base := func(at, sequence int, state, note string) Archive {
		row := Row{}
		for k, v := range map[string]any{"id": id, "workspace_id": ws, "server_id": server, "environment_id": nil, "generation": 3, "plan_id": nil, "request_key": key, "kind": "crosshost", "parent_id": nil, "command": command, "state": state, "report_sequence": sequence, "cancel_requested": false, "steps": []any{}, "note": note} {
			setValue(row, k, v)
		}
		return Archive{Version: 5, Workspace: ws, Name: "Recovery", CreatedAt: time.Date(2026, 1, 1, 0, at, 0, 0, time.UTC), Tables: map[string][]Row{"execution_operations": {row}}}
	}
	a, b, c := base(1, 1, "succeeded", "Original"), base(3, 1, "running", "Fenced fork"), base(2, 1, "succeeded", "Retained receipt")
	setValue(a.Tables["execution_operations"][0], "steps", []any{map[string]any{"name": "complete", "state": "succeeded"}})
	combine := func(input ...Archive) (Archive, error) {
		var bases RecoveryBases
		for _, a := range input {
			if err := bases.Add(a); err != nil {
				return Archive{}, err
			}
		}
		return bases.Archive(), nil
	}
	left, err := combine(a, b, c)
	if err != nil {
		t.Fatal("redacted diagnostics conflicted with identical progress", err)
	}
	right, err := combine(c, b, a)
	if err != nil || !reflect.DeepEqual(left, right) || string(left.Tables["execution_operations"][0]["note"]) != `"Retained receipt"` {
		t.Fatal("progress metadata lost its own capture time", err)
	}
	// Equal-time diagnostic redaction also has one canonical result.
	c.CreatedAt = a.CreatedAt
	left, err = combine(a, c)
	if err != nil {
		t.Fatal(err)
	}
	right, err = combine(c, a)
	if err != nil || !reflect.DeepEqual(left, right) {
		t.Fatal("equal-time diagnostics changed selection by input order", err)
	}
	for _, metadata := range []bool{false, true} {
		a, b, c = base(1, 1, "running", "A"), base(2, 2, "running", "B"), base(3, 1, "needs_attention", "C")
		if metadata {
			a.Tables["execution_operations"], b.Tables["execution_operations"], c.Tables["execution_operations"] = nil, nil, nil
			project := uuid.New()
			for i, archive := range []*Archive{&a, &b, &c} {
				row := Row{}
				setValue(row, "id", project)
				setValue(row, "name", []string{"A", "B", "C"}[i])
				archive.Tables["projects"] = []Row{row}
			}
			c.CreatedAt = a.CreatedAt
		}
		for _, input := range [][]Archive{{a, b, c}, {a, c, b}, {c, b, a}} {
			if _, err := combine(input...); err == nil {
				t.Fatal("a newer row hid contradictory older evidence", metadata)
			}
		}
	}
}

func TestRecoveryBasesAcceptEquivalentRemovalAcknowledgements(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	c := api.ExecutionCommand{Id: id, ServerId: uuid.New(), Kind: "visual", Generation: 4}
	row := Row{}
	for key, value := range map[string]any{"id": id, "workspace_id": ws, "server_id": c.ServerId, "environment_id": nil, "generation": 4, "plan_id": nil, "request_key": uuid.New(), "kind": "visual", "parent_id": nil, "command": c, "state": "succeeded", "report_sequence": 1, "cancel_requested": false, "runtime_outcome": "removed", "runtime_plan_id": nil} {
		setValue(row, key, value)
	}
	transferred := maps.Clone(row)
	setValue(transferred, "state", "cancelled")
	setValue(transferred, "report_sequence", 0)
	for _, input := range [][]Row{{row, transferred}, {transferred, row}} {
		var combined RecoveryBases
		for _, r := range input {
			if err := combined.Add(Archive{Version: 6, Workspace: ws, Name: "Recovery", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Tables: map[string][]Row{"execution_operations": {r}}}); err != nil {
				t.Fatal("equivalent cleanup acknowledgement rejected", err)
			}
		}
		result := combined.Archive().Tables["execution_operations"][0]
		if string(result["runtime_outcome"]) != `"removed"` {
			t.Fatal("cleanup outcome erased")
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

// Every base keeps unfinished work, so one an earlier base holds unfinished and a later base leaves
// out had ended by then. Its stale copy must not come back as unfinished. An operation every base
// keeps whatever its state can only be missing from a base made on another history, so it stays.
func TestRecoveryBasesDropUnfinishedWorkALaterBaseLeftOut(t *testing.T) {
	ws, server := uuid.New(), uuid.New()
	operation := func(kind, action string, parent *uuid.UUID) (Row, string) {
		id := uuid.New()
		command := api.ExecutionCommand{Id: id, ServerId: server, Kind: api.ExecutionCommandKind(kind), Generation: 1, Deadline: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
		if action != "" {
			network := jsontext.Value(`{"action":"` + action + `"}`)
			command.Network = &network
		}
		row := Row{}
		for key, value := range map[string]any{"id": id, "workspace_id": ws, "server_id": server, "environment_id": nil, "generation": 1, "plan_id": nil, "request_key": uuid.New(), "kind": kind, "parent_id": parent, "command": command, "state": "running", "report_sequence": 1, "steps": []any{}, "cancel_requested": false, "runtime_outcome": nil, "runtime_plan_id": nil} {
			setValue(row, key, value)
		}
		key, err := RecoveryRowKey("execution_operations", row)
		if err != nil {
			t.Fatal(err)
		}
		return row, key
	}
	ended, endedKey := operation("network", "configure", nil)
	root, rootKey := operation("crosshost", "", nil)
	removal, removalKey := operation("network", "restrict", nil)
	named, namedKey := operation("visual", "", nil)
	applied, appliedKey := operation("visual", "", nil)
	setValue(applied, "runtime_outcome", "removed")
	child, childKey := operation("network", "restrict", nil)
	setValue(child, "parent_id", root["id"])
	request := Row{}
	setValue(request, "id", uuid.New())
	setValue(request, "operation_id", named["id"])
	older := Archive{Version: 6, Workspace: ws, Name: "Recovery", CreatedAt: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Tables: map[string][]Row{"execution_operations": {ended, root, removal, named, applied, child}, "visual_requests": {request}}}
	newer := Archive{Version: 6, Workspace: ws, Name: "Recovery", CreatedAt: time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC), Tables: map[string][]Row{}}
	for _, order := range [][]Archive{{older, newer}, {newer, older}} {
		var combined RecoveryBases
		for _, a := range order {
			if err := combined.Add(a); err != nil {
				t.Fatal(err)
			}
		}
		kept := map[string]bool{}
		for _, row := range combined.Archive().Tables["execution_operations"] {
			key, _ := RecoveryRowKey("execution_operations", row)
			kept[key] = true
		}
		if kept[endedKey] {
			t.Fatal("an unfinished copy a later base contradicts came back")
		}
		if !kept[rootKey] || !kept[removalKey] || !kept[namedKey] || !kept[appliedKey] || !kept[childKey] {
			t.Fatal("a later fork erased work every base keeps")
		}
	}
	// With no later base, the unfinished copy is all there is to go on.
	var alone RecoveryBases
	if err := alone.Add(older); err != nil {
		t.Fatal(err)
	}
	if len(alone.Archive().Tables["execution_operations"]) != 6 {
		t.Fatal("a single base lost unfinished work")
	}
}
