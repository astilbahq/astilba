package execution

import (
	"errors"
	"maps"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryAccountKnowledge keeps a storage account's newer credential, which only a replacement by
// the owner moves on, and the peers either copy lists.
func recoveryAccountKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	known, err := recoveryValue[int64](prior, "generation")
	if err != nil {
		return nil, err
	}
	offered, err := recoveryValue[int64](incoming, "generation")
	if err != nil {
		return nil, err
	}
	out := maps.Clone(prior)
	if offered > known {
		out = maps.Clone(incoming)
	}
	peers, err := recoveryValue[[]uuid.UUID](prior, "peers")
	if err != nil {
		return nil, err
	}
	more, err := recoveryValue[[]uuid.UUID](incoming, "peers")
	if err != nil {
		return nil, err
	}
	for _, p := range more {
		if !slices.Contains(peers, p) && len(peers) < 16 {
			peers = append(peers, p)
		}
	}
	recoverySet(out, "peers", peers)
	return out, nil
}

// keyStates orders a storage key's states: a key moves forward, so the copy further along knows
// more, and revocation, once either copy has begun it, always finishes. Revoking ranks above
// revoked because the account check moves a revoked key whose user AWS still has back to
// revoking, and revoking a user that is gone again is harmless.
var keyStates = map[string]int{"minting": 0, "active": 1, "revoked": 2, "revoking": 3}

var errRecoveryKeyState = errors.New("storage key state conflicts with known management metadata")

// recoveryKeyKnowledge keeps the copy of a storage key further along, the later revocation its
// handover is bound to, and the install tries of the copy that has had more: the operations of
// every earlier try are in the history, so a key that forgot them would walk through each again.
func recoveryKeyKnowledge(prior, incoming portability.Row) (portability.Row, error) {
	was, err := recoveryValue[string](prior, "state")
	if err != nil {
		return nil, err
	}
	offered, err := recoveryValue[string](incoming, "state")
	if err != nil {
		return nil, err
	}
	a, knownState := keyStates[was]
	b, offeredState := keyStates[offered]
	if !knownState || !offeredState {
		return nil, errRecoveryKeyState
	}
	out := maps.Clone(prior)
	if b > a {
		out = maps.Clone(incoming)
	}
	x, err := recoveryValue[*int64](prior, "handed_over")
	if err != nil {
		return nil, err
	}
	y, err := recoveryValue[*int64](incoming, "handed_over")
	if err != nil {
		return nil, err
	}
	if x == nil || y != nil && *y > *x {
		x = y
	}
	recoverySet(out, "handed_over", x)
	tried, err := recoveryValue[int64](prior, "install_attempts")
	if err != nil {
		return nil, err
	}
	more, err := recoveryValue[int64](incoming, "install_attempts")
	if err != nil {
		return nil, err
	}
	if more > tried {
		out["install_attempts"], out["install_next_at"] = incoming["install_attempts"], incoming["install_next_at"]
	} else {
		out["install_attempts"], out["install_next_at"] = prior["install_attempts"], prior["install_next_at"]
	}
	return out, nil
}
