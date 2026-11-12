package secretbroker

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"
)

// Rotation rewraps every stored value under the active key, across all workspaces, for an
// operator's command. It is one record for the whole run.
type Rotation struct {
	session *Session
	mu      sync.Mutex
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
	rule := rules[Rotate]
	return &Rotation{session: &Session{broker: b, id: id, request: r, rule: rule, deadline: b.now().Add(rule.limit), ring: b.backend.Keyring()}}, nil
}

// Rewrap keeps one value and its data key, and wraps the key under the active key. ws is the
// value's workspace, or uuid.Nil for the installation's own.
func (r *Rotation) Rewrap(ws uuid.UUID, item Item, ciphertext []byte) ([]byte, error) {
	identity := item.identity(ws)
	if identity == "" || !slices.Contains(r.session.rule.seal, item.Kind) {
		return nil, ErrNotAllowed
	}
	r.session.mu.Lock()
	ring := r.session.ring
	expired := !r.session.broker.now().Before(r.session.deadline)
	r.session.mu.Unlock()
	if ring == nil || expired {
		return nil, ErrNeedKeys
	}
	return ring.Rewrap(ciphertext, identity)
}

func (r *Rotation) Close(ctx context.Context, outcome Outcome) { r.session.Close(ctx, outcome) }
