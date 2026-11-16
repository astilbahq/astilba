package execution

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
)

func TestARecordOutdatesAnApprovalOnlyWhenChangedToSomethingElseAfterIt(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	approval := storedApproval{"198.51.100.1", "", &at}
	for _, c := range []struct {
		kind, content string
		modified      time.Time
		outdated      bool
	}{
		// The approved address, whenever it was written.
		{"A", "198.51.100.1", at.Add(time.Hour), false},
		// Another address, written before the approval or within its margin: an older approval's,
		// or a job's late request, which the next job replaces.
		{"A", "198.51.100.2", at.Add(-time.Hour), false},
		{"A", "198.51.100.2", at.Add(outdatedMargin), false},
		// Written after the margin: the zone is newer than the approval.
		{"A", "198.51.100.2", at.Add(outdatedMargin + time.Millisecond), true},
		// A type the approval holds no address for, added after it.
		{"AAAA", "2001:db8::1", at.Add(time.Hour), true},
		{"AAAA", "2001:db8::1", at.Add(time.Minute), false},
	} {
		if got := approval.outdates(c.kind, c.content, c.modified); got != c.outdated {
			t.Errorf("%s %s at %v: outdated %v", c.kind, c.content, c.modified.Sub(at), got)
		}
	}
	// An approval given before its time was recorded is older than any record.
	unknown := storedApproval{"198.51.100.1", "", nil}
	if !unknown.outdates("A", "198.51.100.2", at.Add(-24*time.Hour)) || unknown.outdates("A", "198.51.100.1", at) {
		t.Fatal("an approval of unknown age was judged by time")
	}
}

func TestARecordTheLastJobObservedIsLostOnlyWhenNothingExplainsItsAbsence(t *testing.T) {
	at := time.Now()
	both := storedApproval{"198.51.100.1", "2001:db8::1", &at}
	observed := map[string]string{"A": "a", "AAAA": "b"}
	a := applicationdns.Listed{ID: "a", Type: "A", Content: "198.51.100.1", ModifiedOn: at}
	aaaa := applicationdns.Listed{ID: "c", Type: "AAAA", Content: "2001:db8::1", ModifiedOn: at}
	for _, c := range []struct {
		why      string
		approval storedApproval
		observed map[string]string
		audited  map[string]bool
		present  []applicationdns.Listed
		lost     bool
	}{
		{"both still there", both, observed, nil, []applicationdns.Listed{a, {ID: "b", Type: "AAAA", Content: "2001:db8::1", ModifiedOn: at}}, false},
		{"removed outside this database", both, observed, nil, []applicationdns.Listed{a}, true},
		{"every record removed", both, observed, nil, nil, true},
		{"removed by this database", both, observed, map[string]bool{"b": true}, []applicationdns.Listed{a}, false},
		{"replaced by another record of ours", both, observed, nil, []applicationdns.Listed{a, aaaa}, false},
		{"the approval no longer holds that address", storedApproval{"198.51.100.1", "", &at}, observed, nil, []applicationdns.Listed{a}, false},
		{"never observed, or observed in another zone", both, map[string]string{}, nil, nil, false},
	} {
		if got := c.approval.lost(c.observed, c.audited, c.present); got != c.lost {
			t.Errorf("%s: lost %v", c.why, got)
		}
	}
}
