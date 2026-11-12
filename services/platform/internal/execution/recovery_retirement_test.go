package execution

import (
	"maps"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func recoveryEnvironmentRow(state string, operation *uuid.UUID, withoutHost bool) portability.Row {
	row := portability.Row{}
	recoverySet(row, "id", uuid.Nil)
	recoverySet(row, "state", state)
	recoverySet(row, "retirement_operation_id", operation)
	recoverySet(row, "retired_without_host", withoutHost)
	if state == "active" {
		recoverySet(row, "retired_at", nil)
		recoverySet(row, "retired_by", nil)
	} else {
		recoverySet(row, "retired_at", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
		recoverySet(row, "retired_by", uuid.New())
	}
	return row
}

// Recovery takes a checkpoint's retirement only when management has not seen its operation: a
// database restored from before the retirement ends retired, and one that has seen it keeps what
// came after, such as a restore.
func TestRecoveryTakesOnlyANewerRetirement(t *testing.T) {
	retirement := uuid.New()
	key, err := portability.RecoveryRowKey("execution_operations", portability.Row{"id": recoveryEnvironmentRow("retired", &retirement, false)["retirement_operation_id"]})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]portability.Row{key: {}}
	for _, tc := range []struct {
		name            string
		prior, incoming portability.Row
		operations      map[string]portability.Row
		want            string
	}{
		{"local database from before the retirement", recoveryEnvironmentRow("active", nil, false), recoveryEnvironmentRow("retired", &retirement, false), nil, "retired"},
		{"checkpoint taken while retiring", recoveryEnvironmentRow("active", nil, false), recoveryEnvironmentRow("retiring", &retirement, false), nil, "retiring"},
		{"local database restored after the retirement", recoveryEnvironmentRow("active", nil, false), recoveryEnvironmentRow("retired", &retirement, false), seen, "active"},
		{"the same retirement completed since", recoveryEnvironmentRow("retiring", &retirement, false), recoveryEnvironmentRow("retired", &retirement, false), seen, "retired"},
		{"the same retirement already completed locally", recoveryEnvironmentRow("retired", &retirement, false), recoveryEnvironmentRow("retiring", &retirement, false), seen, "retired"},
		{"a retirement that needed no operation", recoveryEnvironmentRow("active", nil, false), recoveryEnvironmentRow("retired", nil, true), nil, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := maps.Clone(tc.prior)
			if err := recoveryRetirementKnowledge(selected, tc.prior, tc.incoming, tc.operations); err != nil {
				t.Fatal(err)
			}
			state, err := recoveryValue[string](selected, "state")
			if err != nil || state != tc.want {
				t.Fatalf("state %q, want %q", state, tc.want)
			}
			source := tc.prior
			if state != mustRecoveryState(t, tc.prior) {
				source = tc.incoming
			}
			for _, field := range recoveryRetirementFields {
				if string(selected[field]) != string(source[field]) {
					t.Fatalf("%s was not taken with the rest of the retirement", field)
				}
			}
		})
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
