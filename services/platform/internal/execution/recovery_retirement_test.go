package execution

import (
	"maps"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func recoveryEnvironmentRow(state string, operation *uuid.UUID, withoutHost bool, generation int64) portability.Row {
	row := portability.Row{}
	recoverySet(row, "id", uuid.Nil)
	recoverySet(row, "state", state)
	recoverySet(row, "retirement_operation_id", operation)
	recoverySet(row, "retired_without_host", withoutHost)
	recoverySet(row, "execution_generation", generation)
	if state == "active" {
		recoverySet(row, "retired_at", nil)
		recoverySet(row, "retired_by", nil)
	} else {
		recoverySet(row, "retired_at", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
		recoverySet(row, "retired_by", uuid.New())
	}
	return row
}

func recoveryOperationRows(t *testing.T, generations map[uuid.UUID]int64) map[string]portability.Row {
	t.Helper()
	out := map[string]portability.Row{}
	for id, generation := range generations {
		row := portability.Row{}
		recoverySet(row, "id", id)
		recoverySet(row, "generation", generation)
		key, err := portability.RecoveryRowKey("execution_operations", row)
		if err != nil {
			t.Fatal(err)
		}
		out[key] = row
	}
	return out
}

// Recovery takes a checkpoint's retirement only when it is newer than what management knows: a
// database restored from before a retirement ends retired, a checkpoint restored after the
// database's retirement ends active, and a checkpoint that predates what management knows leaves it
// alone.
func TestRecoveryTakesOnlyANewerRetirement(t *testing.T) {
	retirement, later := uuid.New(), uuid.New()
	// The retirement raised the environment's generation to 4, and a later one to 7.
	seen := recoveryOperationRows(t, map[uuid.UUID]int64{retirement: 4})
	both := recoveryOperationRows(t, map[uuid.UUID]int64{retirement: 4, later: 7})
	for _, tc := range []struct {
		name            string
		prior, incoming portability.Row
		operations      map[string]portability.Row
		want            string
	}{
		{"local database from before the retirement", recoveryEnvironmentRow("active", nil, false, 3), recoveryEnvironmentRow("retired", &retirement, false, 4), nil, "retired"},
		{"checkpoint taken while retiring", recoveryEnvironmentRow("active", nil, false, 3), recoveryEnvironmentRow("retiring", &retirement, false, 4), nil, "retiring"},
		{"local database restored after the retirement", recoveryEnvironmentRow("active", nil, false, 6), recoveryEnvironmentRow("retired", &retirement, false, 4), seen, "active"},
		{"the same retirement completed since", recoveryEnvironmentRow("retiring", &retirement, false, 4), recoveryEnvironmentRow("retired", &retirement, false, 4), seen, "retired"},
		{"the same retirement already completed locally", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("retiring", &retirement, false, 4), seen, "retired"},
		{"a retirement that needed no operation", recoveryEnvironmentRow("active", nil, false, 3), recoveryEnvironmentRow("retired", nil, true, 3), nil, "active"},
		{"checkpoint restored after the local retirement and deployed since", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("active", nil, false, 6), seen, "active"},
		{"checkpoint restored after the local retirement with nothing since", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("active", nil, false, 4), seen, "active"},
		{"checkpoint from before the local retirement", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("active", nil, false, 3), seen, "retired"},
		{"checkpoint retired again without an operation after a restore", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("retired", nil, true, 5), seen, "retired"},
		{"checkpoint retired again by a later operation", recoveryEnvironmentRow("retired", &retirement, false, 4), recoveryEnvironmentRow("retiring", &later, false, 7), seen, "retiring"},
		{"local retirement later than the checkpoint's", recoveryEnvironmentRow("retiring", &later, false, 7), recoveryEnvironmentRow("retired", &retirement, false, 5), both, "retiring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := maps.Clone(tc.prior)
			if err := recoveryRetirementKnowledge(selected, tc.prior, tc.incoming, tc.operations); err != nil {
				t.Fatal(err)
			}
			state := mustRecoveryState(t, selected)
			if state != tc.want {
				t.Fatalf("state %q, want %q", state, tc.want)
			}
			source := tc.prior
			if string(selected["retirement_operation_id"]) != string(tc.prior["retirement_operation_id"]) || state != mustRecoveryState(t, tc.prior) || string(selected["retired_without_host"]) != string(tc.prior["retired_without_host"]) {
				source = tc.incoming
			}
			for _, field := range recoveryRetirementFields {
				if string(selected[field]) != string(source[field]) {
					t.Fatalf("%s was not taken with the rest of the retirement", field)
				}
			}
			// Replaying the merge over its own result changes nothing.
			again := maps.Clone(selected)
			if err := recoveryRetirementKnowledge(again, selected, tc.incoming, tc.operations); err != nil {
				t.Fatal(err)
			}
			for _, field := range recoveryRetirementFields {
				if string(again[field]) != string(selected[field]) {
					t.Fatalf("replaying the merge changed %s", field)
				}
			}
		})
	}
}

// The foreign key on an environment's retirement keeps the operation it names, so a database without
// that operation is not one recovery can order a checkpoint against.
func TestRecoveryRefusesARetirementWithoutItsOperation(t *testing.T) {
	retirement := uuid.New()
	prior := recoveryEnvironmentRow("retired", &retirement, false, 4)
	if err := recoveryRetirementKnowledge(maps.Clone(prior), prior, recoveryEnvironmentRow("active", nil, false, 6), nil); err == nil {
		t.Fatal("a retirement whose operation is unknown was merged")
	}
}

func mustRecoveryState(t *testing.T, row portability.Row) string {
	t.Helper()
	state, err := recoveryValue[string](row, "state")
	if err != nil {
		t.Fatal(err)
	}
	return state
}
