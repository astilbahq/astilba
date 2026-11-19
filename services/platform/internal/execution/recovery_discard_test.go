package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// Recovery keeps a restore's later discard, by the discards' generations, whichever copy holds it,
// and never drops a discard that one copy doesn't know.
func TestRecoveryKeepsARestoresLaterDiscard(t *testing.T) {
	earlier, later, unknown := uuid.New(), uuid.New(), uuid.New()
	known := recoveryOperationRows(t, map[uuid.UUID]int64{earlier: 4})
	arriving := recoveryOperationRows(t, map[uuid.UUID]int64{earlier: 4, later: 7})
	row := func(discard *uuid.UUID) portability.Row {
		out := portability.Row{}
		recoverySet(out, "discard_operation_id", discard)
		return out
	}
	for name, c := range map[string]struct{ prior, incoming, want *uuid.UUID }{
		"neither copy knows one":      {nil, nil, nil},
		"only management knows one":   {&earlier, nil, &earlier},
		"only the checkpoint has one": {nil, &later, &later},
		"both name the same":          {&later, &later, &later},
		"the checkpoint's is later":   {&earlier, &later, &later},
		"management's is later":       {&later, &earlier, &later},
	} {
		prior := row(c.prior)
		selected := row(c.prior)
		if err := recoveryDiscardKnowledge(selected, prior, row(c.incoming), known, arriving); err != nil {
			t.Fatal(name, err)
		}
		var got *uuid.UUID
		if got, _ = recoveryValue[*uuid.UUID](selected, "discard_operation_id"); (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("%s: kept %v, want %v", name, got, c.want)
		}
	}
	if recoveryDiscardKnowledge(row(&earlier), row(&earlier), row(&unknown), known, arriving) == nil {
		t.Error("a discard whose operation neither copy holds was taken")
	}
}
