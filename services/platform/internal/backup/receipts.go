package backup

import (
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MaxReportedRuns is the most runs a schedule report carries, so the most snapshots it lists.
const MaxReportedRuns = 128

// Receipts are when management first received each snapshot a policy's reports list as available,
// by its own clock, by snapshot ID. Only the policy's server reports for it.
type Receipts map[string]time.Time

// ParseReceipts reads a policy's receipts, refusing any that its reports couldn't have made.
func ParseReceipts(raw []byte) (Receipts, error) {
	var out Receipts
	if json.Unmarshal(raw, &out) != nil || out == nil || len(out) > MaxReportedRuns {
		return nil, errors.New("invalid snapshot receipts")
	}
	for snapshot, at := range out {
		if !SnapshotID(snapshot) || at.IsZero() {
			return nil, errors.New("invalid snapshot receipts")
		}
	}
	return out, nil
}

// Available lists, once each, the snapshots runs list as available, as Prunable counts them: each a
// succeeded run names that hasn't been pruned since. Uploaded lists those an upload names, the
// only runs that make a snapshot: a restore names one without making it.
func Available(runs []Run) (available, uploaded []string) {
	seen := map[string]bool{}
	available, uploaded = []string{}, []string{}
	for _, r := range runs {
		if r.State != "succeeded" || r.Pruned || r.Snapshot == "" {
			continue
		}
		if !seen[r.Snapshot] {
			seen[r.Snapshot] = true
			available = append(available, r.Snapshot)
		}
		if r.TargetID == uuid.Nil {
			uploaded = append(uploaded, r.Snapshot)
		}
	}
	return available, uploaded
}
