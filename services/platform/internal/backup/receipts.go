package backup

import (
	"encoding/json/v2"
	"errors"
	"time"
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
