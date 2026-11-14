package secretbroker

import (
	"context"
	"errors"
	"slices"
	"strconv"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrMissingWrapping is a live key generation with no wrapping for the purpose under the
	// configured backend: a backend switch left part way, or rows changed by hand.
	ErrMissingWrapping = errors.New("a live key generation has no wrapping for this purpose under the configured key backend")
	// ErrKeyCheck is a wrapping that opened to a key other than the one its generation was created
	// with.
	ErrKeyCheck = errors.New("a key wrapping holds a different key than its generation was created with")
	// ErrBackendsChanged is a new key wrapped for backends that changed before it was stored. Making
	// the key again, with the request that needed it, succeeds.
	ErrBackendsChanged = &domain.Error{Code: "temporarily_busy", Message: "The installation's key services changed while this was saved. Try again."}
	// ErrBackendNotConfigured is a backend new keys must be wrapped for that this instance has
	// not configured, so it cannot make keys.
	ErrBackendNotConfigured = errors.New("new keys must be wrapped for a key backend this instance has not configured")
)

// keyRef names one key a session holds: a scope's key at one generation.
type keyRef struct {
	scope      custody.Scope
	generation int64
}

// liveStates are the generations live rows may use, which every session holds. Recovery also
// opens retained ones, which recovery data may still use.
func liveStates(p Purpose) []string {
	if p == Recover {
		return []string{"active", "retiring", "retained"}
	}
	return []string{"active", "retiring"}
}

// sealedKey is one generation's wrapping for a session's purpose.
type sealedKey struct {
	generation int64
	kcv        []byte
	wrapped    []byte
}

// wrappedKeys reads the generations a request's session holds, and their wrappings for its
// purpose under the configured backend. Only an active, sealable generation has a seal wrapping,
// so a seal session holds no other.
func (b *Broker) wrappedKeys(ctx context.Context, r Request) (custody.Scope, []sealedKey, error) {
	scope := custody.ScopeWorkspace
	if r.Workspace == uuid.Nil {
		scope = custody.ScopeInstallation
	}
	if _, ok := custody.ClassFor(scope, string(r.Purpose)); !ok {
		return scope, nil, nil
	}
	var out []sealedKey
	if scope == custody.ScopeWorkspace {
		rows, err := b.q.WorkspaceKeyWrappings(ctx, db.WorkspaceKeyWrappingsParams{Purpose: string(r.Purpose), BackendID: b.backend.ID(), WorkspaceID: r.Workspace, States: liveStates(r.Purpose)})
		if err != nil {
			return scope, nil, err
		}
		for _, row := range rows {
			out = append(out, sealedKey{row.Generation, row.Kcv, row.Wrapped})
		}
	} else {
		rows, err := b.q.InstallationKeyWrappings(ctx, db.InstallationKeyWrappingsParams{Purpose: string(r.Purpose), BackendID: b.backend.ID(), Scope: string(scope), States: liveStates(r.Purpose)})
		if err != nil {
			return scope, nil, err
		}
		for _, row := range rows {
			out = append(out, sealedKey{row.Generation, row.Kcv, row.Wrapped})
		}
	}
	held := out[:0]
	for _, k := range out {
		switch {
		case k.wrapped != nil:
			held = append(held, k)
		case r.Purpose != Seal:
			return scope, nil, ErrMissingWrapping
		}
	}
	return scope, held, nil
}

// unwrap opens each wrapping, and checks each opens to its generation's key.
func (b *Broker) unwrap(ctx context.Context, scope custody.Scope, holder uuid.UUID, purpose string, wrapped []sealedKey) (map[keyRef][]byte, error) {
	keys := map[keyRef][]byte{}
	if len(wrapped) == 0 {
		return keys, nil
	}
	if b.installation == uuid.Nil {
		return nil, errors.New("custody is not installed")
	}
	class, _ := custody.ClassFor(scope, purpose)
	for _, k := range wrapped {
		opened, err := b.backend.Unwrap(ctx, class, k.wrapped, custody.Context{Installation: b.installation, Scope: scope, Workspace: holder, Generation: k.generation, Purpose: purpose})
		if err == nil && !custody.CheckKey(opened.Key, k.kcv) {
			clear(opened.Key)
			err = ErrKeyCheck
		}
		if err != nil {
			for _, key := range keys {
				clear(key)
			}
			return nil, err
		}
		keys[keyRef{scope, k.generation}] = opened.Key
	}
	return keys, nil
}

// generationsOf lists the generations a session holds for its record, with the installation
// keyring, which v1 values are under, as generation 0.
func generationsOf(keys []sealedKey) []int64 {
	out := []int64{0}
	for _, k := range keys {
		out = append(out, k.generation)
	}
	return out
}

// NewKeys is a new key's first generation: made, wrapped for every purpose and for every backend
// new keys are wrapped for, and stored only by the transaction that creates its holder. Close it
// once that transaction has ended, whatever the outcome.
type NewKeys struct {
	broker    *Broker
	scope     custody.Scope
	holder    uuid.UUID
	version   int64
	backend   string
	kcv       []byte
	wrappings map[string][]byte
	source    string
	key       []byte
	session   *Session
	stored    bool
}

// Create makes a new workspace's first key generation, before the transaction that creates the
// workspace, since wrapping waits on the key service. That transaction stores it through
// store.CreateWorkspace. r names the new workspace and who creates it. When it also names values,
// the keys serve as a seal session for that transaction, recorded as Begin records one. Like
// Begin, it must not run with a transaction open.
func (b *Broker) Create(ctx context.Context, r Request) (*NewKeys, error) {
	if r.Workspace == uuid.Nil || !r.Principal.wellFormed() {
		return nil, errors.New("a new workspace's keys need its ID and who creates it")
	}
	session := len(r.Items) > 0 || len(r.Kinds) > 0
	var rule rule
	if session {
		var err error
		r.Purpose, r.NewWorkspace = Seal, true
		if rule, err = b.admit(ctx, r); err != nil {
			return nil, err
		}
		if !b.budgets.take(budgetKeys(r)) {
			return nil, domain.RateLimited
		}
	}
	k, err := b.newKeys(ctx, custody.ScopeWorkspace, r.Workspace, "new-workspace")
	if err != nil || !session {
		return k, err
	}
	id := uuid.New()
	if err = b.record(ctx, id, r, []int64{0, 1}); err != nil {
		k.Close(ctx, Refused)
		return nil, err
	}
	// The session holds the new key itself, which it seals under once values are sealed as v2.
	k.session = &Session{broker: b, id: id, request: r, rule: rule, deadline: b.now().Add(rule.limit), ring: b.backend.Keyring(), keys: map[keyRef][]byte{{custody.ScopeWorkspace, 1}: slices.Clone(k.key)}}
	b.log.Info("secret access", "access", id, "workspace", r.Workspace, "purpose", r.Purpose, "principal", r.Principal.Kind, "items", len(r.Items), "kinds", len(r.Kinds), "backend", b.backend.ID())
	return k, nil
}

// newKeys makes and wraps generation 1 of a key. holder is the workspace, or uuid.Nil for the
// installation's own scopes.
func (b *Broker) newKeys(ctx context.Context, scope custody.Scope, holder uuid.UUID, source string) (*NewKeys, error) {
	if b.pool == nil {
		return nil, errors.New("custody is not installed")
	}
	q := db.New(b.pool)
	version, err := q.CustodyBackendsVersion(ctx)
	if err != nil {
		return nil, err
	}
	backends, err := q.WriteBackends(ctx)
	if err != nil {
		return nil, err
	}
	// The broker has one backend configured, so it can wrap only for that one.
	if !slices.Equal(backends, []string{b.backend.ID()}) {
		return nil, ErrBackendNotConfigured
	}
	key, kcv, err := custody.NewKey()
	if err != nil {
		return nil, err
	}
	k := &NewKeys{broker: b, scope: scope, holder: holder, version: version, backend: b.backend.ID(), kcv: kcv, wrappings: map[string][]byte{}, source: source, key: key}
	for _, purpose := range custody.Purposes(scope) {
		class, _ := custody.ClassFor(scope, purpose)
		k.wrappings[purpose], _, err = b.backend.Wrap(ctx, class, key, custody.Context{Installation: b.installation, Scope: scope, Workspace: holder, Generation: 1, Purpose: purpose})
		if err != nil {
			clear(key)
			return nil, err
		}
	}
	return k, nil
}

// Workspace is the workspace the keys are for.
func (k *NewKeys) Workspace() uuid.UUID { return k.holder }

// Session is the seal session Create was asked for, or nil.
func (k *NewKeys) Session() *Session { return k.session }

// Insert stores the keys, in the transaction that creates their holder. It holds the backends'
// version until that commits, and refuses keys wrapped for backends that have changed since.
func (k *NewKeys) Insert(ctx context.Context, q *db.Queries) error {
	if k.stored {
		return errors.New("new keys are stored once")
	}
	version, err := q.ShareCustodyBackendsVersion(ctx)
	if err != nil {
		return err
	}
	if version != k.version {
		return ErrBackendsChanged
	}
	if k.scope == custody.ScopeWorkspace {
		if err = q.InsertWorkspaceKey(ctx, db.InsertWorkspaceKeyParams{WorkspaceID: k.holder, Generation: 1, Kcv: k.kcv}); err != nil {
			return err
		}
	} else if err = q.InsertInstallationKey(ctx, db.InsertInstallationKeyParams{Scope: string(k.scope), Generation: 1, Kcv: k.kcv}); err != nil {
		return err
	}
	for _, purpose := range custody.Purposes(k.scope) {
		if k.scope == custody.ScopeWorkspace {
			err = q.InsertWorkspaceKeyWrapping(ctx, db.InsertWorkspaceKeyWrappingParams{WorkspaceID: k.holder, Generation: 1, Purpose: purpose, BackendID: k.backend, Wrapped: k.wrappings[purpose]})
		} else {
			err = q.InsertInstallationKeyWrapping(ctx, db.InsertInstallationKeyWrappingParams{Scope: string(k.scope), Generation: 1, Purpose: purpose, BackendID: k.backend, Wrapped: k.wrappings[purpose]})
		}
		if err != nil {
			return err
		}
	}
	e := keyEvent{run: uuid.New(), kind: "key-created", scope: k.scope, generation: 1, details: map[string]string{"source": k.source}}
	if k.scope == custody.ScopeWorkspace {
		e.workspace = &k.holder
	} else {
		// The events table names installation scope for both of the installation's own keys.
		e.scope, e.details["key"] = custody.ScopeInstallation, string(k.scope)
	}
	if err = k.broker.event(ctx, q, e); err != nil {
		return err
	}
	k.stored = true
	return nil
}

// Close lets go of the key, and ends the seal session with outcome if there is one.
func (k *NewKeys) Close(ctx context.Context, outcome Outcome) {
	clear(k.key)
	if k.session != nil {
		k.session.Close(ctx, outcome)
	}
}

// keysStage is the custody stage creating every key completes: every workspace, the
// installation and the token keys have a generation.
const keysStage = 2

// InitKeys makes generation 1 of the installation and token keys, and of the key of every
// workspace that has none, until it runs out of time. It reports whether any remain. Several may
// run at once: each holder is taken under a lock that others skip, and its keys are stored only if
// it still has none. Once nothing remains, it records the custody stage as complete.
func (b *Broker) InitKeys(ctx context.Context, more func() bool) (bool, error) {
	if b.pool == nil {
		return false, errors.New("custody is not installed")
	}
	q := db.New(b.pool)
	skipped := false
	for _, scope := range []custody.Scope{custody.ScopeInstallation, custody.ScopeToken} {
		has, err := q.InstallationScopeHasKeys(ctx, string(scope))
		if err != nil {
			return true, err
		}
		if has {
			continue
		}
		made, err := b.initHolder(ctx, scope, uuid.Nil)
		if err != nil {
			return true, err
		}
		skipped = skipped || !made
	}
	var after uuid.UUID
	for more() {
		ids, err := q.WorkspacesWithoutKeys(ctx, db.WorkspacesWithoutKeysParams{After: after, Batch: initBatch})
		if err != nil {
			return true, err
		}
		for _, ws := range ids {
			made, err := b.initHolder(ctx, custody.ScopeWorkspace, ws)
			if err != nil {
				return true, err
			}
			skipped = skipped || !made
			after = ws
			if !more() {
				return true, nil
			}
		}
		if len(ids) < initBatch {
			break
		}
	}
	if skipped || !more() {
		return true, nil
	}
	progress, err := q.KeyProgress(ctx)
	if err != nil {
		return true, err
	}
	if progress.WithKeys < progress.Workspaces || !progress.Installation || !progress.Token {
		return true, nil
	}
	n, err := q.AdvanceCustodyStage(ctx, keysStage-1)
	if err == nil && n == 1 {
		err = b.event(ctx, q, keyEvent{run: uuid.New(), kind: "stage-completed", details: map[string]string{"stage": strconv.Itoa(keysStage)}})
	}
	return false, err
}

// initBatch is how many workspaces InitKeys reads at a time.
const initBatch = 100

// initHolder makes and stores generation 1 for one holder that has none, and reports whether it
// did. It skips a holder another worker holds, and one that has keys or is gone by the time its
// transaction runs.
func (b *Broker) initHolder(ctx context.Context, scope custody.Scope, holder uuid.UUID) (bool, error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	lock := string(scope) + ":" + holder.String()
	locked, err := db.New(conn).TryLockKeyCreation(ctx, lock)
	if err != nil || !locked {
		return false, err
	}
	defer func() {
		if _, err := db.New(conn).UnlockKeyCreation(context.WithoutCancel(ctx), lock); err != nil {
			// A lock that cannot be released goes with the connection.
			b.log.Warn("key creation lock not released; closing its connection", "holder", lock, "error", err.Error())
			conn.Hijack().Close(context.WithoutCancel(ctx))
		}
	}()
	k, err := b.newKeys(ctx, scope, holder, "existing")
	if err != nil {
		return false, err
	}
	defer k.Close(ctx, Used)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	var has bool
	if scope == custody.ScopeWorkspace {
		if _, err = q.LockWorkspaceByID(ctx, holder); errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		} else if err != nil {
			return false, err
		}
		has, err = q.WorkspaceHasKeys(ctx, holder)
	} else {
		if err = q.LockCustody(ctx); err != nil {
			return false, err
		}
		has, err = q.InstallationScopeHasKeys(ctx, string(scope))
	}
	if err != nil || has {
		return err == nil, err
	}
	if err = k.Insert(ctx, q); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// openV2 opens a v2 envelope with the key its header names, which must be one the session holds
// for the item's scope.
func openV2(keys map[keyRef][]byte, item Item, header cryptobox.Header, ciphertext []byte, identity string) ([]byte, error) {
	want := custody.ScopeWorkspace
	if item.installation() {
		want = custody.ScopeInstallation
	}
	if custody.Scope(header.Scope) != want {
		return nil, cryptobox.ErrKey
	}
	key, ok := keys[keyRef{want, header.Generation}]
	if !ok {
		return nil, ErrNeedKeys
	}
	return cryptobox.OpenUnder(key, ciphertext, identity)
}
