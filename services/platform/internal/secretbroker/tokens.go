package secretbroker

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// TokenKind is a self-issued token sealed under the resident token key. Only the kinds below
// exist: a caller cannot name another identity and open a stored value with this key.
type TokenKind struct{ identity string }

var (
	DeviceToken = TokenKind{"login-device:v1"}
	ResetMail   = TokenKind{"reset-mail:v1"}
)

// ErrTokenKeyNotLoaded is a token under a token key generation this instance does not hold yet, or
// could not unwrap when it last tried. It is worth trying again: instances load new generations
// every minute, and sooner once a token asks for one.
var ErrTokenKeyNotLoaded = errors.New("the token key generation is not loaded yet")

// tokenReload is how often an instance reads the token key generations, and tokenWakeGap the least
// time between reads a token asking for an unknown generation can cause.
const (
	tokenReload  = time.Minute
	tokenWakeGap = 5 * time.Second
)

// Tokens seals and opens short-lived tokens management issues to itself. They are not stored
// customer values, and needing the key service on every sign-in page would add nothing to the
// record, so these keys stay resident: the installation keyring, and every token key generation
// not retired, each recorded in the installation's key history as it is loaded. Tokens are issued
// under the keyring until values are sealed as v2.
type Tokens struct {
	broker   *Broker
	ring     *cryptobox.Keyring
	instance uuid.UUID
	wake     chan struct{}

	mu   sync.RWMutex
	keys map[int64][]byte
	// seen is the highest generation the database has shown, and failed those it showed that
	// could not be unwrapped. A token under either may open after the next load.
	seen   int64
	failed map[int64]bool
}

// Tokens records the resident keys, then loads them. Like Begin, it must not run inside a
// transaction.
func (b *Broker) Tokens(ctx context.Context) (*Tokens, error) {
	t := &Tokens{broker: b, ring: b.backend.Keyring(), instance: uuid.New(), wake: make(chan struct{}, 1), keys: map[int64][]byte{}, failed: map[int64]bool{}}
	id := uuid.New()
	if err := b.record(ctx, id, Request{Purpose: Token, Principal: System("token-keys")}, []int64{0}); err != nil {
		return nil, err
	}
	// The keys are now resident for this process's life, which is the record's whole story.
	if err := b.outcome(ctx, id, Released); err != nil {
		return nil, err
	}
	if err := t.load(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// load unwraps every token key generation not retired that the instance does not hold, lets go of
// any since retired, and reports what it holds.
func (t *Tokens) load(ctx context.Context) error {
	b := t.broker
	if b.pool == nil {
		// Custody is not installed, as in unit tests: only the keyring is resident.
		return nil
	}
	q := db.New(b.pool)
	rows, err := q.InstallationKeyWrappings(ctx, db.InstallationKeyWrappingsParams{Purpose: "token", BackendID: b.backend.ID(), Scope: string(custody.ScopeToken), States: []string{"active", "retiring", "retained"}})
	if err != nil {
		return err
	}
	live := map[int64]bool{}
	var failures []error
	for _, row := range rows {
		live[row.Generation] = true
		t.mu.RLock()
		_, held := t.keys[row.Generation]
		t.mu.RUnlock()
		if held {
			continue
		}
		key, err := t.unwrap(ctx, q, row)
		t.mu.Lock()
		t.seen = max(t.seen, row.Generation)
		if err != nil {
			t.failed[row.Generation] = true
			failures = append(failures, err)
		} else {
			delete(t.failed, row.Generation)
			t.keys[row.Generation] = key
		}
		t.mu.Unlock()
	}
	t.mu.Lock()
	var generations []int64
	for g, key := range t.keys {
		if !live[g] {
			clear(key)
			delete(t.keys, g)
			continue
		}
		generations = append(generations, g)
	}
	t.mu.Unlock()
	slices.Sort(generations)
	if err = q.RecordCustodyInstance(ctx, db.RecordCustodyInstanceParams{InstanceID: t.instance, TokenGenerations: append([]int64{}, generations...)}); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// unwrap records a token key generation's loading, then unwraps it and checks it.
func (t *Tokens) unwrap(ctx context.Context, q *db.Queries, row db.InstallationKeyWrappingsRow) ([]byte, error) {
	b := t.broker
	if row.Wrapped == nil {
		return nil, ErrMissingWrapping
	}
	if err := b.event(ctx, q, keyEvent{run: t.instance, kind: "token-key-loaded", scope: custody.ScopeInstallation, generation: row.Generation, details: map[string]string{"key": string(custody.ScopeToken)}}); err != nil {
		return nil, err
	}
	class, _ := custody.ClassFor(custody.ScopeToken, "token")
	opened, err := b.backend.Unwrap(ctx, class, row.Wrapped, custody.Context{Installation: b.installation, Scope: custody.ScopeToken, Generation: row.Generation, Purpose: "token"})
	if err != nil {
		return nil, err
	}
	if !custody.CheckKey(opened.Key, row.Kcv) {
		clear(opened.Key)
		return nil, ErrKeyCheck
	}
	return opened.Key, nil
}

// Run loads new token key generations every minute, and sooner when a token asks for one this
// instance does not hold, until ctx ends.
func (t *Tokens) Run(ctx context.Context) {
	ticker := time.NewTicker(tokenReload)
	defer ticker.Stop()
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-t.wake:
			if wait := tokenWakeGap - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
		last = time.Now()
		load, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := t.load(load); err != nil && ctx.Err() == nil {
			t.broker.log.Warn("token keys not loaded", "instance", t.instance, "error", err.Error())
		}
		if err := db.New(t.broker.pool).PruneCustodyInstances(load); err != nil && ctx.Err() == nil {
			t.broker.log.Warn("stopped instances not pruned", "error", err.Error())
		}
		cancel()
	}
}

func (t *Tokens) Seal(kind TokenKind, plaintext []byte) ([]byte, error) {
	if kind.identity == "" {
		return nil, ErrNotAllowed
	}
	return t.ring.Seal(plaintext, kind.identity)
}

// Open opens a token sealed under the keyring or under a token key generation. ErrTokenKeyNotLoaded
// is a generation this instance may yet load, and is worth retrying.
func (t *Tokens) Open(kind TokenKind, ciphertext []byte) ([]byte, error) {
	if kind.identity == "" {
		return nil, ErrNotAllowed
	}
	header, err := cryptobox.ReadHeader(ciphertext)
	if err != nil {
		return nil, err
	}
	if header.Version == 1 {
		return t.ring.Open(ciphertext, kind.identity)
	}
	if header.Scope != cryptobox.ScopeToken {
		return nil, cryptobox.ErrKey
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if key, ok := t.keys[header.Generation]; ok {
		return cryptobox.OpenUnder(key, ciphertext, kind.identity)
	}
	if header.Generation > t.seen || t.failed[header.Generation] {
		select {
		case t.wake <- struct{}{}:
		default:
		}
		return nil, ErrTokenKeyNotLoaded
	}
	// A generation the database showed and has since retired: its tokens have all expired.
	return nil, cryptobox.ErrKey
}

// Instance is the ID this process reports its token keys under.
func (t *Tokens) Instance() uuid.UUID { return t.instance }
