package secretbroker

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	// sealing is the generation new values are sealed under: the active one, while it was
	// sealable at Begin and the session holds it. 0 is none.
	sealing int64
	// keyless is a session whose holder had no key when it began, so a v2 value it meets was
	// sealed under a key made since, which a new session holds.
	keyless bool
	// stale is a session that met keys newer than it holds, which a new session would hold.
	stale bool
	// sealed are the checkpoints a seal session sealed, the only values it opens.
	sealed map[[sha256.Size]byte]bool
	// requests are the key service's IDs for the unwraps that gave the session its keys.
	requests []string
}

// Live reports whether the session can still be used.
func (s *Session) Live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring != nil && !s.stale && (s.deadline.IsZero() || s.broker.now().Before(s.deadline))
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
// live generation of its scope's key (v2). ErrNeedKeys means the session has ended, or holds
// older keys than the value needs, and the caller begins another; ErrNotAllowed means the item
// was never part of the request. A seal session opens only a checkpoint it sealed itself, to prove
// it reads back.
func (s *Session) Open(item Item, ciphertext []byte) ([]byte, error) {
	ring, identity, err := s.allowed(item, s.rule.open)
	if err != nil {
		return nil, err
	}
	if s.request.Purpose == Seal && !s.sealedHere(ciphertext) {
		return nil, ErrNotAllowed
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
	if s.keyless {
		if s.request.Purpose == Recover {
			// Recovery runs with the installation offline, where no key is made: the restored
			// database never had the key the value was sealed under.
			return nil, ErrKeyNotInBackup
		}
		// The value is under a key made after this session began.
		s.stale = true
		return nil, ErrKeysChanged
	}
	return openV2(s.keys, item, header, ciphertext, identity)
}

func (s *Session) sealedHere(ciphertext []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealed[sha256.Sum256(ciphertext)]
}

// Fence is the transaction a sealed value is stored in. Sealing holds the generation it seals
// under in it, so the generation stays the one new values are sealed under until the value is
// stored. *db.Queries provides it.
type Fence interface {
	ShareSealableWorkspaceKey(ctx context.Context, arg db.ShareSealableWorkspaceKeyParams) (int64, error)
	ShareSealableInstallationKey(ctx context.Context, arg db.ShareSealableInstallationKeyParams) (int64, error)
	WorkspaceHasKeys(ctx context.Context, workspaceID uuid.UUID) (bool, error)
	InstallationScopeHasKeys(ctx context.Context, scope string) (bool, error)
}

// Seal seals a new value for an item under the generation of its scope's key that new values are
// sealed under, and holds that generation in q, the transaction that stores the value. Store the
// value in that same transaction. A transaction that takes the workspace lock takes it before
// sealing, the order that changing a key's generations takes. ErrNeedKeys means the generation
// changed since the session began, and the caller begins another.
func (s *Session) Seal(ctx context.Context, q Fence, item Item, plaintext []byte) ([]byte, error) {
	_, identity, err := s.allowed(item, s.rule.seal)
	if err != nil {
		return nil, err
	}
	ref, err := s.fence(ctx, q, item)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[ref]
	if !ok {
		return nil, ErrNeedKeys
	}
	sealed, err := cryptobox.SealUnder(key, string(ref.scope), ref.generation, plaintext, identity)
	if err == nil && item.Kind == RecoveryCheckpoint {
		if s.sealed == nil {
			s.sealed = map[[sha256.Size]byte]bool{}
		}
		s.sealed[sha256.Sum256(sealed)] = true
	}
	return sealed, err
}

// Rehome wraps a stored value's data key again under the generation new values are sealed under,
// as Seal does for a new value, and leaves its data as it is. Recovery uses it, so that values it
// restores never land under a generation that is no longer active.
func (s *Session) Rehome(ctx context.Context, q Fence, item Item, ciphertext []byte) ([]byte, error) {
	ring, identity, err := s.allowed(item, s.rule.seal)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(s.rule.open, item.Kind) {
		return nil, ErrNotAllowed
	}
	header, err := cryptobox.ReadHeader(ciphertext)
	if err != nil {
		return nil, err
	}
	ref, err := s.fence(ctx, q, item)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[ref]
	if !ok {
		return nil, ErrNeedKeys
	}
	var from []byte
	if header.Version == 2 {
		if custody.Scope(header.Scope) != ref.scope {
			return nil, cryptobox.ErrKey
		}
		if from, ok = s.keys[keyRef{ref.scope, header.Generation}]; !ok {
			return nil, cryptobox.ErrKey
		}
	}
	return cryptobox.MoveUnder(ring, from, ciphertext, identity, key, string(ref.scope), ref.generation)
}

// fence holds the session's sealing generation in q, or says why there is none: no key yet, or
// one that changed since the session began.
func (s *Session) fence(ctx context.Context, q Fence, item Item) (keyRef, error) {
	scope := custody.ScopeWorkspace
	if item.installation() {
		scope = custody.ScopeInstallation
	}
	s.mu.Lock()
	ref := keyRef{scope, s.sealing}
	s.mu.Unlock()
	if ref.generation == 0 {
		return ref, s.noSealingKey(ctx, q, scope)
	}
	var err error
	if scope == custody.ScopeWorkspace {
		_, err = q.ShareSealableWorkspaceKey(ctx, db.ShareSealableWorkspaceKeyParams{WorkspaceID: s.request.Workspace, Generation: ref.generation})
	} else {
		_, err = q.ShareSealableInstallationKey(ctx, db.ShareSealableInstallationKeyParams{Scope: string(scope), Generation: ref.generation})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		s.markStale()
		return ref, ErrKeysChanged
	}
	return ref, err
}

// noSealingKey is why a session holds no generation to seal under. A holder given its first key
// since the session began is a session to begin again. One still without keys waits for the job
// that makes them, which it wakes. One whose only key may no longer be sealed under waits for a new
// generation.
func (s *Session) noSealingKey(ctx context.Context, q Fence, scope custody.Scope) error {
	var has bool
	var err error
	if scope == custody.ScopeWorkspace {
		has, err = q.WorkspaceHasKeys(ctx, s.request.Workspace)
	} else {
		has, err = q.InstallationScopeHasKeys(ctx, string(scope))
	}
	if err != nil {
		return err
	}
	if has && s.keyless {
		s.markStale()
		return ErrKeysChanged
	}
	if has {
		return ErrNoSealableKey
	}
	if s.request.Purpose == Recover {
		return ErrKeyMissing
	}
	s.broker.wantKeys()
	s.broker.log.Warn("value refused until its key exists", "access", s.id, "workspace", s.request.Workspace, "scope", scope)
	return ErrKeyPending
}

func (s *Session) markStale() {
	s.mu.Lock()
	s.stale = true
	s.mu.Unlock()
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
	if err := s.broker.outcome(ctx, s.id, outcome, s.requests); err != nil {
		// The sweep records it as abandoned instead.
		s.broker.log.Error("secret access outcome not recorded", "access", s.id, "error", err.Error())
		return
	}
	s.broker.log.Info("secret access closed", "access", s.id, "workspace", s.request.Workspace, "purpose", s.request.Purpose, "outcome", outcome)
}
