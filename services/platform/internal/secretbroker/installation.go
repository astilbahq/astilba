package secretbroker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pool is a database the broker's custody commands use directly, with transactions of their own.
// *pgxpool.Pool is one.
type Pool interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

var (
	// ErrInstallationMismatch is a database whose installation ID is not the one configured: a copy
	// restored for another purpose, or the wrong database.
	ErrInstallationMismatch = errors.New("the database belongs to another installation than ASTILBA_INSTALLATION_ID names")
	// ErrBackendNotRegistered is a configured key backend the installation has not registered, or
	// is retiring.
	ErrBackendNotRegistered = errors.New("the configured key backend is not one this installation wraps its keys for")
	// ErrCanaryMismatch is a canary the configured backend cannot open: a different keyring or
	// provider key, or a database copied under another installation ID.
	ErrCanaryMismatch = errors.New("the configured key backend cannot open the installation's canary")
	// ErrKeyringMismatch is a keyring that cannot open the values already stored, met before any
	// canary exists to say so: a database restored or upgraded from before canaries.
	ErrKeyringMismatch = errors.New("the configured keyring cannot open the installation's stored secrets")
)

// Install checks the database and the configured backend belong to one installation before any
// key is used, and records what it sets up. pinned is ASTILBA_INSTALLATION_ID, or uuid.Nil when it
// is not set. An operator also checks the classes only operators unwrap under.
//
// The first instance to start registers its backend, and the first start with each backend
// makes its canaries. Afterwards a different keyring, an unregistered backend, or a database
// whose installation ID differs from the configured one refuses to start, and the refusal is
// recorded.
func (b *Broker) Install(ctx context.Context, pool Pool, pinned uuid.UUID, operator bool) error {
	q := db.New(pool)
	id, err := q.InstallationID(ctx)
	if err != nil {
		return err
	}
	run := uuid.New()
	refuse := func(err error, details map[string]string) error {
		details["cause"] = causes[err]
		if e := b.event(ctx, q, keyEvent{run: run, kind: "start-refused", details: details}); e != nil {
			b.log.Error("custody refusal not recorded", "reason", e.Error())
		}
		return err
	}
	if pinned != uuid.Nil && pinned != id {
		return refuse(ErrInstallationMismatch, map[string]string{"configured": pinned.String()})
	}
	if err = b.register(ctx, pool, run); err != nil {
		if errors.Is(err, ErrBackendNotRegistered) {
			return refuse(err, map[string]string{})
		}
		return err
	}
	// The canaries already made are checked first, so a wrong keyring never adds one of its own.
	canaries, err := q.InstallationKeyCanaries(ctx, b.backend.ID())
	if err != nil {
		return err
	}
	for _, c := range canaries {
		class := custody.Class(c.Class)
		if !operator && !custody.ServingDecrypts(class) {
			continue
		}
		if err = custody.CheckCanary(ctx, b.backend, class, id, c.Wrapped); err != nil {
			if errors.Is(err, custody.ErrInvalid) || errors.Is(err, custody.ErrDenied) {
				return refuse(ErrCanaryMismatch, map[string]string{"class": c.Class})
			}
			return err
		}
	}
	// A backend's first canaries are what every later start is checked against, so the keyring
	// must first open what is already stored. Otherwise a database upgraded or restored from
	// before canaries, started once with the wrong keyring, would refuse the right one ever after.
	if len(canaries) == 0 {
		if err = b.checkStored(ctx, pool, run); err != nil {
			if errors.Is(err, ErrKeyringMismatch) {
				return refuse(err, map[string]string{})
			}
			return err
		}
	}
	if err = b.makeCanaries(ctx, pool, id, run, canaries); err != nil {
		return err
	}
	b.installation, b.pool = id, pool
	return nil
}

// causes name each refusal in its event, in a word an operator can search for.
var causes = map[error]string{
	ErrInstallationMismatch: "installation-id",
	ErrBackendNotRegistered: "backend",
	ErrCanaryMismatch:       "canary",
	ErrKeyringMismatch:      "keyring",
}

// register makes the configured backend the installation's first, or checks it is already one the
// installation wraps for. Instances starting together agree on one under the custody lock.
func (b *Broker) register(ctx context.Context, pool Pool, run uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	if err = q.LockCustody(ctx); err != nil {
		return err
	}
	backends, err := q.CustodyBackends(ctx)
	if err != nil {
		return err
	}
	if len(backends) > 0 {
		i := slices.IndexFunc(backends, func(r db.CustodyBackendsRow) bool { return r.BackendID == b.backend.ID() })
		if i < 0 || backends[i].Role == "retiring" {
			return ErrBackendNotRegistered
		}
		return nil
	}
	if err = q.RegisterCustodyBackend(ctx, b.backend.ID()); err != nil {
		return err
	}
	if err = b.event(ctx, q, keyEvent{run: run, kind: "backend-registered", details: map[string]string{"role": "read+write"}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// makeCanaries wraps a canary for every class the backend has none for among existing. The
// wrapping uses the key service, so it happens before the transaction that stores it; an
// instance that lost the race keeps the canary already stored.
func (b *Broker) makeCanaries(ctx context.Context, pool Pool, installation, run uuid.UUID, existing []db.InstallationKeyCanariesRow) error {
	var err error
	made := map[custody.Class][]byte{}
	for _, class := range custody.Classes {
		if slices.ContainsFunc(existing, func(r db.InstallationKeyCanariesRow) bool { return r.Class == string(class) }) {
			continue
		}
		if made[class], err = custody.NewCanary(ctx, b.backend, class, installation); err != nil {
			return err
		}
	}
	if len(made) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Instances racing here wait on each other's rows: only the first insert of a class counts,
	// and records its event in the same transaction.
	q := db.New(tx)
	for _, class := range custody.Classes {
		blob, ok := made[class]
		if !ok {
			continue
		}
		n, err := q.InsertInstallationKeyCanary(ctx, db.InsertInstallationKeyCanaryParams{BackendID: b.backend.ID(), Class: string(class), Wrapped: blob})
		if err != nil {
			return err
		}
		if n == 1 {
			if err = b.event(ctx, q, keyEvent{run: run, kind: "canary-created", details: map[string]string{"class": string(class)}}); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// checkStored opens one stored value of every kind a rewrap covers. Like a rewrap, it is key
// maintenance across every workspace: one record, naming the start's run, whose key history says
// how the check went.
func (b *Broker) checkStored(ctx context.Context, pool Pool, run uuid.UUID) error {
	values, err := db.New(pool).SampleSealedValues(ctx)
	if err != nil || len(values) == 0 {
		return err
	}
	r := Request{Purpose: Rotate, Principal: Operator(run.String()), Kinds: append(slices.Clone(workspaceKinds), installationKinds...), Reason: "keyring-check"}
	id := uuid.New()
	if err = b.record(ctx, id, r); err != nil {
		return err
	}
	ring := b.backend.Keyring()
	outcome := Used
	for _, v := range values {
		value, err := ring.Open(v.Sealed, Item{Kind: Kind(v.Kind), ID: v.ID}.identity(v.WorkspaceID))
		clear(value)
		if err != nil {
			outcome = Failed
			break
		}
	}
	if err = b.outcome(ctx, id, outcome); err != nil {
		return err
	}
	if outcome == Failed {
		return ErrKeyringMismatch
	}
	return b.event(ctx, db.New(pool), keyEvent{run: run, kind: "keyring-checked", details: map[string]string{"values": strconv.Itoa(len(values))}})
}

// keyEvent is one row of the installation's key history.
type keyEvent struct {
	run              uuid.UUID
	kind             string
	operator, reason string
	details          map[string]string
}

// event records a key event. It never fails silently: a command that cannot record what it does
// does not do it.
func (b *Broker) event(ctx context.Context, q *db.Queries, e keyEvent) error {
	if e.details == nil {
		e.details = map[string]string{}
	}
	details, err := stablejson.Marshal(e.details)
	if err != nil {
		return err
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return q.InsertInstallationKeyEvent(write, db.InsertInstallationKeyEventParams{
		ID: uuid.New(), RunID: e.run, Kind: e.kind, Operator: optionalText(e.operator), Reason: optionalText(e.reason),
		BackendID: optionalText(b.backend.ID()), Details: details,
	})
}

// Status is the installation's custody as `astilba custody status` reports it.
type Status struct {
	Installation uuid.UUID
	// Pinned is whether configuration names the installation ID, and Matches whether it names this one.
	Pinned, Matches bool
	Backend         string
	Backends        []db.CustodyBackendsRow
	// Canaries holds each class's state: ok, missing, or the error opening it gave.
	Canaries map[custody.Class]string
	Stage    int32
	// Unfinished are commands that recorded a start and no finish.
	Unfinished []db.UnfinishedCustodyRunsRow
}

// Status reads the installation's custody and checks every canary, changing nothing.
func (b *Broker) Status(ctx context.Context, pool Pool, pinned uuid.UUID) (Status, error) {
	q := db.New(pool)
	var s Status
	var err error
	if s.Installation, err = q.InstallationID(ctx); err != nil {
		return s, err
	}
	s.Pinned, s.Matches, s.Backend = pinned != uuid.Nil, pinned == uuid.Nil || pinned == s.Installation, b.backend.ID()
	if s.Backends, err = q.CustodyBackends(ctx); err != nil {
		return s, err
	}
	canaries, err := q.InstallationKeyCanaries(ctx, b.backend.ID())
	if err != nil {
		return s, err
	}
	s.Canaries = map[custody.Class]string{}
	for _, class := range custody.Classes {
		s.Canaries[class] = "missing"
	}
	for _, c := range canaries {
		state := "ok"
		if err := custody.CheckCanary(ctx, b.backend, custody.Class(c.Class), s.Installation, c.Wrapped); err != nil {
			state = err.Error()
		}
		s.Canaries[custody.Class(c.Class)] = state
	}
	if s.Stage, err = q.CustodyStage(ctx); err != nil {
		return s, err
	}
	s.Unfinished, err = q.UnfinishedCustodyRuns(ctx)
	return s, err
}

// Pending lists what an operator still has to do, or what the next start does, in plain words.
func (s Status) Pending() []string {
	var out []string
	if !s.Matches {
		out = append(out, "ASTILBA_INSTALLATION_ID names another installation: nothing starts until it names this one")
	} else if !s.Pinned {
		out = append(out, fmt.Sprintf("ASTILBA_INSTALLATION_ID is not set: set it to %s so a database copied from another installation is refused", s.Installation))
	}
	registered := slices.IndexFunc(s.Backends, func(r db.CustodyBackendsRow) bool { return r.BackendID == s.Backend })
	switch {
	case len(s.Backends) == 0:
		out = append(out, "no key backend is registered: the next start registers "+s.Backend)
	case registered < 0 || s.Backends[registered].Role == "retiring":
		out = append(out, "the configured key backend "+s.Backend+" is not registered for new keys: nothing starts with it")
	}
	for _, class := range custody.Classes {
		switch state := s.Canaries[class]; state {
		case "ok":
		case "missing":
			out = append(out, "the "+string(class)+" canary is missing: the next start makes it")
		default:
			out = append(out, "the "+string(class)+" canary does not open ("+state+"): nothing starts until the configured keys are the installation's")
		}
	}
	for _, run := range s.Unfinished {
		out = append(out, fmt.Sprintf("%s run %s at %s has no finish, and no run since has succeeded: it stopped part way or is running now", run.Kind, run.RunID, run.At.UTC().Format(time.RFC3339)))
	}
	return out
}
