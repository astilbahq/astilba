package secretbroker

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// Rotation rewraps every stored value under the active key, across all workspaces, for an
// operator's command. It is one record for the whole run.
type Rotation struct {
	session *Session
}

// BeginRotation records the run before it touches any key. Like Begin, it must not run inside a
// transaction.
func (b *Broker) BeginRotation(ctx context.Context, operator string) (*Rotation, error) {
	p := Operator(operator)
	if !p.wellFormed() {
		return nil, errors.New("an operator reference is required")
	}
	all := append(slices.Clone(workspaceKinds), installationKinds...)
	r := Request{Purpose: Rotate, Principal: p, Kinds: all}
	id := uuid.New()
	if err := b.record(ctx, id, r); err != nil {
		return nil, err
	}
	// An operator's command holds the keys until it ends, which on a large installation can be
	// longer than any session limit: it has no deadline, and the process exits after it.
	return &Rotation{session: &Session{broker: b, id: id, request: r, rule: rules[Rotate], ring: b.backend.Keyring()}}, nil
}

// Rewrap keeps one value and its data key, and wraps the key under the active key. ws is the
// value's workspace, or uuid.Nil for the installation's own.
func (r *Rotation) Rewrap(ws uuid.UUID, item Item, ciphertext []byte) ([]byte, error) {
	identity := item.identity(ws)
	if identity == "" {
		return nil, ErrNotAllowed
	}
	r.session.mu.Lock()
	ring := r.session.ring
	r.session.mu.Unlock()
	if ring == nil {
		return nil, ErrNeedKeys
	}
	return ring.Rewrap(ciphertext, identity)
}

func (r *Rotation) Close(ctx context.Context, outcome Outcome) { r.session.Close(ctx, outcome) }
