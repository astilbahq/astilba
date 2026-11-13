package secretbroker

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// Rotation rewraps every stored value under the active key, across all workspaces, for an
// operator's command. The installation's key history records its start and finish under one run,
// and the tenant-facing record names the run, never the operator.
type Rotation struct {
	session  *Session
	run      uuid.UUID
	values   atomic.Int64
	canaries int
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
	if err := b.record(ctx, id, r); err != nil {
		b.finish(ctx, run, "rewrap-finished", Failed, nil)
		return nil, err
	}
	// An operator's command holds the keys until it ends, which on a large installation can be
	// longer than any session limit: it has no deadline, and the process exits after it.
	return &Rotation{run: run, session: &Session{broker: b, id: id, request: r, rule: rules[Rotate], ring: b.backend.Keyring()}}, nil
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

// Close ends the session and records the run's finish. A run that succeeded also records how many
// wrappings it replaced; a failed one rolled its values back, so its counts would mislead.
func (r *Rotation) Close(ctx context.Context, outcome Outcome) {
	r.session.Close(ctx, outcome)
	var details map[string]string
	if outcome == Used {
		details = map[string]string{"values": strconv.FormatInt(r.values.Load(), 10), "canaries": strconv.Itoa(r.canaries)}
	}
	r.session.broker.finish(ctx, r.run, "rewrap-finished", outcome, details)
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
