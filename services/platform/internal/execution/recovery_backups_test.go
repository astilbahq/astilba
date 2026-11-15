package execution

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
)

// Whichever copy holds the newer report, recovery keeps a receipt for each snapshot that report
// lists, at the earlier of the two copies' times, and none for a snapshot only the older one lists.
func TestRecoveryKeepsTheNewerReportsReceiptsAtTheEarlierTimes(t *testing.T) {
	early, late := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	row := func(sequence int64, receipts backup.Receipts) portability.Row {
		out := portability.Row{}
		recoverySet(out, "configuration", backup.Policy{Generation: 1})
		recoverySet(out, "status", backup.Status{Generation: 1})
		recoverySet(out, "status_sequence", sequence)
		recoverySet(out, "snapshot_receipts", receipts)
		return out
	}
	older, newer := row(1, backup.Receipts{a: early, b: early}), row(2, backup.Receipts{a: late, c: late})
	for _, pair := range [][2]portability.Row{{older, newer}, {newer, older}} {
		out, err := recoveryPolicyKnowledge(pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		got, err := recoveryValue[backup.Receipts](out, "snapshot_receipts")
		if err != nil || !maps.EqualFunc(got, backup.Receipts{a: early, c: late}, time.Time.Equal) {
			t.Errorf("recovery kept %v, %v", got, err)
		}
	}
}
