package secretbroker

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

// Outcome is how a session ended.
type Outcome string

const (
	Released  Outcome = "released"
	Used      Outcome = "used"
	Refused   Outcome = "refused"
	Failed    Outcome = "failed"
	Abandoned Outcome = "abandoned"
)

// UsedIf is Used when the session's keys did their work, and Refused when the work was turned
// down before they were needed.
func UsedIf(used bool) Outcome {
	if used {
		return Used
	}
	return Refused
}

// Done is Used when the work the keys served succeeded, and Failed otherwise.
func Done(err error) Outcome {
	if err != nil {
		return Failed
	}
	return Used
}

// Session holds keys for one request. It is never shared between requests or kept after them.
// Open and Seal are local work, so they may run inside a transaction.
type Session struct {
	broker   *Broker
	id       uuid.UUID
	request  Request
	rule     rule
	deadline time.Time

	mu   sync.Mutex
	ring *cryptobox.Keyring
	// keys are the scope's live generations the session unwrapped, which v2 values are under.
	keys map[keyRef][]byte
}

// Live reports whether the session can still be used.
func (s *Session) Live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring != nil && (s.deadline.IsZero() || s.broker.now().Before(s.deadline))
}

// ID is the record this session was given under.
func (s *Session) ID() uuid.UUID { return s.id }

// Workspace is the workspace the session serves.
func (s *Session) Workspace() uuid.UUID { return s.request.Workspace }

// allowed returns the keyring for one item, or why the session cannot use it.
func (s *Session) allowed(item Item, kinds []Kind) (*cryptobox.Keyring, string, error) {
	identity := item.identity(s.request.Workspace)
	declared := slices.Contains(s.request.Kinds, item.Kind) || slices.ContainsFunc(s.request.Items, func(i Item) bool {
		return i.Kind == item.Kind && i.ID == item.ID && i.checkpoint == item.checkpoint
	})
	if identity == "" || !declared || !slices.Contains(kinds, item.Kind) {
		return nil, "", ErrNotAllowed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ring == nil || !s.deadline.IsZero() && !s.broker.now().Before(s.deadline) {
		return nil, "", ErrNeedKeys
	}
	return s.ring, identity, nil
}

// Open returns an item's value, whether it is sealed under the installation keyring (v1) or a
// live generation of its scope's key (v2). ErrNeedKeys means the session has ended and the caller
// begins another; ErrNotAllowed means the item was never part of the request.
func (s *Session) Open(item Item, ciphertext []byte) ([]byte, error) {
	ring, identity, err := s.allowed(item, s.rule.open)
	if err != nil {
		return nil, err
	}
	header, err := cryptobox.ReadHeader(ciphertext)
	if err != nil {
		return nil, err
	}
	if header.Version == 1 {
		return ring.Open(ciphertext, identity)
	}
	// Close clears the keys in place, so a v2 open holds the lock while it uses one.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return nil, ErrNeedKeys
	}
	return openV2(s.keys, item, header, ciphertext, identity)
}

// Seal stores a new value for an item, under the installation keyring until values are sealed as
// v2.
func (s *Session) Seal(item Item, plaintext []byte) ([]byte, error) {
	ring, identity, err := s.allowed(item, s.rule.seal)
	if err != nil {
		return nil, err
	}
	return ring.Seal(plaintext, identity)
}

// Close lets go of the keys and records how the session ended. Only the first call records.
func (s *Session) Close(ctx context.Context, outcome Outcome) {
	s.mu.Lock()
	open := s.ring != nil
	s.ring = nil
	for _, key := range s.keys {
		clear(key)
	}
	s.keys = nil
	s.mu.Unlock()
	if !open {
		return
	}
	if err := s.broker.outcome(ctx, s.id, outcome); err != nil {
		// The sweep records it as abandoned instead.
		s.broker.log.Error("secret access outcome not recorded", "access", s.id, "error", err.Error())
		return
	}
	s.broker.log.Info("secret access closed", "access", s.id, "workspace", s.request.Workspace, "purpose", s.request.Purpose, "outcome", outcome)
}
