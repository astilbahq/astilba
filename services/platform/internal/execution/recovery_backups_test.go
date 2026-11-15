package execution

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// Whichever copy holds the newer report, recovery keeps a receipt for each snapshot that report
// lists as available, at the earlier time of the copies that have one, including a time only the
// older copy has, and none for a snapshot the newer report no longer lists.
func TestRecoveryKeepsTheNewerReportsReceiptsAtTheEarlierTimes(t *testing.T) {
	early, late := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, b, c, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	run := func(snapshot string, restore bool) backup.Run {
		r := backup.Run{State: "succeeded", Snapshot: snapshot}
		if restore {
			r.TargetID = uuid.New()
		}
		return r
	}
	row := func(sequence int64, runs []backup.Run, receipts backup.Receipts) portability.Row {
		out := portability.Row{}
		recoverySet(out, "configuration", backup.Policy{Generation: 1})
		recoverySet(out, "status", backup.Status{Generation: 1, Runs: runs})
		recoverySet(out, "status_sequence", sequence)
		recoverySet(out, "snapshot_receipts", receipts)
		return out
	}
	// The newer report lists a and c by their uploads, and d only by a restore that verified it after
	// its upload left the history, which only the older copy received.
	older := row(1, []backup.Run{run(a, false), run(b, false), run(d, false)}, backup.Receipts{a: early, b: early, d: early})
	newer := row(2, []backup.Run{run(a, false), run(c, false), run(d, true)}, backup.Receipts{a: late, c: late})
	for _, pair := range [][2]portability.Row{{older, newer}, {newer, older}} {
		out, err := recoveryPolicyKnowledge(pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		got, err := recoveryValue[backup.Receipts](out, "snapshot_receipts")
		if err != nil || !maps.EqualFunc(got, backup.Receipts{a: early, c: late, d: early}, time.Time.Equal) {
			t.Errorf("recovery kept %v, %v", got, err)
		}
	}
}
