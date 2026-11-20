package execution

import (
	"maps"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
)

func recoveryPolicyKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	a, err := recoveryValue[backup.Policy](prior, "configuration")
	if err != nil {
		return nil, err
	}
	b, err := recoveryValue[backup.Policy](incoming, "configuration")
	if err != nil {
		return nil, err
	}
	out := maps.Clone(prior)
	if b.Generation > a.Generation {
		out["configuration"], out["encrypted_access"] = incoming["configuration"], incoming["encrypted_access"]
	}
	sa, err := recoveryValue[backup.Status](prior, "status")
	if err != nil {
		return nil, err
	}
	sb, err := recoveryValue[backup.Status](incoming, "status")
	if err != nil {
		return nil, err
	}
	qa, err := recoveryValue[int64](prior, "status_sequence")
	if err != nil {
		return nil, err
	}
	qb, err := recoveryValue[int64](incoming, "status_sequence")
	if err != nil {
		return nil, err
	}
	kept := sa
	if sb.Generation > sa.Generation || sb.Generation == sa.Generation && qb > qa {
		out["status"], out["status_sequence"], kept = incoming["status"], incoming["status_sequence"], sb
	}
	return out, recoveryReceipts(out, kept, prior, incoming)
}

// recoveryReceipts keeps a receipt for each snapshot the status out keeps lists as available, at
// the earlier time of the two copies that have one: each is a time management received it, so
// the earlier bounds when it existed.
func recoveryReceipts(out portability.Row, kept backup.Status, a, b portability.Row) error {
	ra, err := recoveryValue[backup.Receipts](a, "snapshot_receipts")
	if err != nil {
		return err
	}
	rb, err := recoveryValue[backup.Receipts](b, "snapshot_receipts")
	if err != nil {
		return err
	}
	receipts := backup.Receipts{}
	available, _ := backup.Available(kept.Runs)
	for _, snapshot := range available {
		at, ok := ra[snapshot]
		if other, found := rb[snapshot]; found && (!ok || other.Before(at)) {
			at, ok = other, true
		}
		if ok {
			receipts[snapshot] = at
		}
	}
	recoverySet(out, "snapshot_receipts", receipts)
	return nil
}
