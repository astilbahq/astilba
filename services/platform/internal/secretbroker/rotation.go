package secretbroker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// Rotation rewraps every stored value under the active key, across all workspaces, for an
// operator's command. The installation's key history records its start and finish under one run,
// and the tenant-facing record names the run, never the operator.
type Rotation struct {
	session   *Session
	run       uuid.UUID
	values    atomic.Int64
	canaries  int
	wrappings int
}

// BeginRotation records the run before it touches any key. Like Begin, it must not run inside a
// transaction. Install must have run, with the operator's checks.
func (b *Broker) BeginRotation(ctx context.Context, operator, reason string) (*Rotation, error) {
	if b.pool == nil {
		return nil, errors.New("custody is not installed")
	}
	if operator == "" || len(operator) > 200 || len(reason) > 1000 {
		return nil, errors.New("an operator name of at most 200 characters is required, and a reason is at most 1000")
	}
	run := uuid.New()
	if err := b.event(ctx, db.New(b.pool), keyEvent{run: run, kind: "rewrap-started", operator: operator, reason: reason}); err != nil {
		return nil, err
	}
	all := append(slices.Clone(workspaceKinds), installationKinds...)
	r := Request{Purpose: Rotate, Principal: Operator(run.String()), Kinds: all}
	id := uuid.New()
	if err := b.record(ctx, id, r, []int64{0}); err != nil {
		b.finish(ctx, run, "rewrap-finished", Failed, nil)
		return nil, err
	}
	// An operator's command holds the keys until it ends, which on a large installation can be
	// longer than any session limit: it has no deadline, and the process exits after it.
	return &Rotation{run: run, session: &Session{broker: b, id: id, request: r, rule: rules[Rotate], ring: b.backend.Keyring()}}, nil
}

// Rewrap keeps one value and its data key, and wraps the key under the active key. ws is the
// value's workspace, or uuid.Nil for the installation's own. A v2 value's data key is under its
// scope's key, whose wrappings RewrapWrappings moves instead, so it comes back as it was.
func (r *Rotation) Rewrap(ws uuid.UUID, item Item, ciphertext []byte) ([]byte, error) {
	identity := item.identity(ws)
	if identity == "" {
		return nil, ErrNotAllowed
	}
	if header, err := cryptobox.ReadHeader(ciphertext); err == nil && header.Version == 2 {
		return ciphertext, nil
	}
	r.session.mu.Lock()
	ring := r.session.ring
	r.session.mu.Unlock()
	if ring == nil {
		return nil, ErrNeedKeys
	}
	blob, err := ring.Rewrap(ciphertext, identity)
	if err == nil {
		r.values.Add(1)
	}
	return blob, err
}

// RewrapCanaries wraps the backend's canaries again, so they keep opening once the key that made
// them leaves the keyring. It uses the key service, so it must not run inside a transaction.
func (r *Rotation) RewrapCanaries(ctx context.Context) error {
	b := r.session.broker
	q := db.New(b.pool)
	canaries, err := q.InstallationKeyCanaries(ctx, b.backend.ID())
	if err != nil {
		return err
	}
	for _, c := range canaries {
		blob, err := custody.RewrapCanary(ctx, b.backend, custody.Class(c.Class), b.installation, c.Wrapped)
		if err != nil {
			return err
		}
		n, err := q.RewrapInstallationKeyCanary(ctx, db.RewrapInstallationKeyCanaryParams{BackendID: b.backend.ID(), Class: c.Class, Wrapped: blob, Previous: c.Wrapped})
		if err != nil {
			return err
		}
		// Another rewrap replaced it first, under the same key.
		if n == 1 {
			r.canaries++
		}
	}
	return nil
}

// RewrapWrappings wraps every workspace, installation and token key again under its class's
// current key, so they keep opening once the key that wrapped them leaves the keyring. Like
// RewrapCanaries, it uses the key service and must not run inside a transaction.
func (r *Rotation) RewrapWrappings(ctx context.Context) error {
	b := r.session.broker
	q := db.New(b.pool)
	rewrap := func(scope custody.Scope, holder uuid.UUID, generation int64, purpose string, blob []byte, store func(wrapped []byte) (int64, error)) error {
		class, ok := custody.ClassFor(scope, purpose)
		if !ok {
			return fmt.Errorf("%s key generation %d has a %s wrapping, which no key class covers", scope, generation, purpose)
		}
		wrapped, err := custody.Rewrap(ctx, b.backend, class, blob, custody.Context{Installation: b.installation, Scope: scope, Workspace: holder, Generation: generation, Purpose: purpose})
		if err != nil {
			return fmt.Errorf("cannot rewrap %s key generation %d for %s: %w", scope, generation, purpose, err)
		}
		n, err := store(wrapped)
		// Another rewrap replaced it first, under the same key.
		if n == 1 {
			r.wrappings++
		}
		return err
	}
	workspaceKeys, err := q.AllWorkspaceKeyWrappings(ctx, b.backend.ID())
	if err != nil {
		return err
	}
	for _, w := range workspaceKeys {
		if err = rewrap(custody.ScopeWorkspace, w.WorkspaceID, w.Generation, w.Purpose, w.Wrapped, func(wrapped []byte) (int64, error) {
			return q.RewrapWorkspaceKeyWrapping(ctx, db.RewrapWorkspaceKeyWrappingParams{Wrapped: wrapped, WorkspaceID: w.WorkspaceID, Generation: w.Generation, Purpose: w.Purpose, BackendID: b.backend.ID(), Previous: w.Wrapped})
		}); err != nil {
			return err
		}
	}
	installationKeys, err := q.AllInstallationKeyWrappings(ctx, b.backend.ID())
	if err != nil {
		return err
	}
	for _, w := range installationKeys {
		if err = rewrap(custody.Scope(w.Scope), uuid.Nil, w.Generation, w.Purpose, w.Wrapped, func(wrapped []byte) (int64, error) {
			return q.RewrapInstallationKeyWrapping(ctx, db.RewrapInstallationKeyWrappingParams{Wrapped: wrapped, Scope: w.Scope, Generation: w.Generation, Purpose: w.Purpose, BackendID: b.backend.ID(), Previous: w.Wrapped})
		}); err != nil {
			return err
		}
	}
	return nil
}

// ValuesRewrapped records how many values the run rewrapped, in the transaction that stores them,
// so the record exists exactly when they do.
func (r *Rotation) ValuesRewrapped(ctx context.Context, q *db.Queries) error {
	return r.session.broker.event(ctx, q, keyEvent{run: r.run, kind: "values-rewrapped", details: map[string]string{"values": strconv.FormatInt(r.values.Load(), 10)}})
}

// Close ends the session and records the run's finish, with how many canaries and key wrappings
// it rewrapped. Each is stored as it is rewrapped, so the counts hold whatever the outcome.
func (r *Rotation) Close(ctx context.Context, outcome Outcome) {
	r.session.Close(ctx, outcome)
	r.session.broker.finish(ctx, r.run, "rewrap-finished", outcome, map[string]string{"canaries": strconv.Itoa(r.canaries), "wrappings": strconv.Itoa(r.wrappings)})
}

// finish records a run's last event. A finish that cannot be recorded leaves the run unfinished
// in `custody status`, which is what an operator then checks.
func (b *Broker) finish(ctx context.Context, run uuid.UUID, kind string, outcome Outcome, details map[string]string) {
	if details == nil {
		details = map[string]string{}
	}
	details["outcome"] = string(outcome)
	if err := b.event(ctx, db.New(b.pool), keyEvent{run: run, kind: kind, details: details}); err != nil {
		b.log.Error("custody run finish not recorded", "run", run, "reason", err.Error())
	}
}
