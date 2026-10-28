package portability

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
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
	original, _ := json.Marshal(older)
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
	after, _ := json.Marshal(older)
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
