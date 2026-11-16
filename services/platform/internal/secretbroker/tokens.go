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
	"github.com/jackc/pgx/v5/pgtype"
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
// under the newest generation every running instance holds, and under the keyring until there is
// one.
type Tokens struct {
	broker   *Broker
	ring     *cryptobox.Keyring
	instance uuid.UUID
	// stage is the custody stage this instance's release completes, which it reports with the
	// generations it holds.
	stage int32
	wake  chan struct{}

	mu   sync.RWMutex
	keys map[int64][]byte
	// seen is the highest generation the database has shown, and failed those it showed that
	// could not be unwrapped. A token under either may open after the next load.
	seen   int64
	failed map[int64]bool
	// issuing is the generation new tokens are sealed under, or 0 for the keyring.
	issuing int64
}

// Tokens records the resident keys, then loads them. stage is the custody stage the running
// release completes. Like Begin, it must not run inside a transaction.
//
// The access record's ID is the instance's: the installation's key history records under it each
// token key generation the instance loads, for its whole life, with the key service's ID for each
// unwrap. Those events are where the requests are recorded, so the record's outcome names none.
func (b *Broker) Tokens(ctx context.Context, stage int32) (*Tokens, error) {
	t := &Tokens{broker: b, ring: b.backend.Keyring(), instance: uuid.New(), stage: stage, wake: make(chan struct{}, 1), keys: map[int64][]byte{}, failed: map[int64]bool{}}
	if err := b.record(ctx, t.instance, Request{Purpose: Token, Principal: System("token-keys")}, []int64{0}); err != nil {
		return nil, err
	}
	err := t.load(ctx)
	if err == nil {
		// Stage 6 could have stopped being true while no instance of this release ran.
		err = b.correctEnvironmentsStage(ctx, true)
	}
	if err == nil {
		// The keys are now resident for this process's life, which is the rest of the record's
		// story.
		err = b.outcome(ctx, t.instance, Released, nil)
	} else if recordErr := b.outcome(ctx, t.instance, Failed, nil); recordErr != nil {
		err = errors.Join(err, recordErr)
	}
	if err != nil {
		t.mu.Lock()
		for _, key := range t.keys {
			clear(key)
		}
		t.keys = nil
		t.mu.Unlock()
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
			if t.issuing == g {
				t.issuing = 0
			}
			continue
		}
		generations = append(generations, g)
	}
	t.mu.Unlock()
	slices.Sort(generations)
	if err = q.RecordCustodyInstance(ctx, db.RecordCustodyInstanceParams{InstanceID: t.instance, TokenGenerations: append([]int64{}, generations...), Stage: pgtype.Int4{Int32: t.stage, Valid: true}}); err != nil {
		return errors.Join(append(failures, err)...)
	}
	// Values are sealed under environment keys only while every instance running opens them: an
	// instance of an earlier release, seen in the last five minutes, holds them back.
	older, err := q.InstancesBelowStage(ctx, readEnvironmentsStage)
	t.broker.environmentsSealable.Store(err == nil && older == 0)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	if err = t.broker.correctEnvironmentsStage(ctx, false); err != nil {
		return errors.Join(append(failures, err)...)
	}
	// Issuing moves to a generation only once every instance running holds it, this one included,
	// so any of them opens what another issues. One that starts later loads every generation
	// before it serves.
	issuing, err := q.TokenIssuingGeneration(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	t.mu.Lock()
	if _, held := t.keys[issuing]; !held {
		issuing = 0
	}
	t.issuing = issuing
	t.mu.Unlock()
	return errors.Join(failures...)
}

// unwrap records a token key generation's loading, then unwraps it and checks it, then records
// how that went with the key service's ID for the request, also when it failed. A key whose
// opening cannot be recorded is let go of, and tried again at the next load.
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
	if err == nil && !custody.CheckKey(opened.Key, row.Kcv) {
		clear(opened.Key)
		err = ErrKeyCheck
	}
	result := keyEvent{run: t.instance, kind: "token-key-opened", scope: custody.ScopeInstallation, generation: row.Generation, details: map[string]string{"key": string(custody.ScopeToken)}}
	if err != nil {
		result.kind, result.details["reason"] = "token-key-not-opened", err.Error()
	}
	if opened.ProviderRequestID != "" {
		result.details["provider_requests"] = opened.ProviderRequestID
	}
	if recordErr := b.event(ctx, q, result); recordErr != nil {
		if err == nil {
			clear(opened.Key)
		}
		return nil, errors.Join(err, recordErr)
	}
	if err != nil {
		return nil, err
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

// Seal issues a token under the generation every running instance holds.
func (t *Tokens) Seal(kind TokenKind, plaintext []byte) ([]byte, error) {
	if kind.identity == "" {
		return nil, ErrNotAllowed
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.issuing == 0 {
		return t.ring.Seal(plaintext, kind.identity)
	}
	return cryptobox.SealUnder(t.keys[t.issuing], cryptobox.ScopeToken, t.issuing, plaintext, kind.identity)
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
