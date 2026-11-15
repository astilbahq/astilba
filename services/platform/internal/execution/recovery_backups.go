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
	if sb.Generation > sa.Generation || sb.Generation == sa.Generation && qb > qa {
		out["status"], out["status_sequence"] = incoming["status"], incoming["status_sequence"]
		err = recoveryReceipts(out, incoming, prior)
	} else {
		err = recoveryReceipts(out, prior, incoming)
	}
	return out, err
}

// recoveryReceipts keeps a receipt for each snapshot the status out keeps lists, from the copy
// that status came from, at the earlier of the two copies' times: each is a time management
// received it, so the earlier bounds when it existed.
func recoveryReceipts(out, kept, other portability.Row) error {
	a, err := recoveryValue[backup.Receipts](kept, "snapshot_receipts")
	if err != nil {
		return err
	}
	b, err := recoveryValue[backup.Receipts](other, "snapshot_receipts")
	if err != nil {
		return err
	}
	for snapshot, at := range a {
		if earlier, ok := b[snapshot]; ok && earlier.Before(at) {
			a[snapshot] = earlier
		}
	}
	recoverySet(out, "snapshot_receipts", a)
	return nil
}
