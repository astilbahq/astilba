package execution

import (
	"maps"
	"time"

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
	}
	return out, nil
}

// recoveryReceiptKnowledge keeps the earlier of two copies' receipts for one snapshot: each is a
// time management received it, so the earlier bounds when it existed.
func recoveryReceiptKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	a, err := recoveryValue[time.Time](prior, "first_reported_at")
	if err != nil {
		return nil, err
	}
	b, err := recoveryValue[time.Time](incoming, "first_reported_at")
	if err != nil {
		return nil, err
	}
	if b.Before(a) {
		return maps.Clone(incoming), nil
	}
	return maps.Clone(prior), nil
}
