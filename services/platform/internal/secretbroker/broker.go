// Package secretbroker is the one path to stored values. Every use names a workspace, a purpose
// and a principal, and is recorded in secret_access before any key is used. The keys then serve
// that one request through a session, and nothing keeps them between requests.
package secretbroker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrNeedKeys is a session that has closed or run out of time: begin another.
	ErrNeedKeys = errors.New("the secret session has ended")
	// ErrNotAllowed is a value the session was not given, which is always a bug in the caller.
	ErrNotAllowed = errors.New("the secret session does not cover this value")
	// ErrTooLarge is a value whose sealed form exceeds what can be opened again.
	ErrTooLarge = cryptobox.ErrTooLarge
)

// ErrKeysChanged is a session whose holder's keys changed after it began: it holds no key yet
// for a value sealed since, or the generation it seals under has been replaced. It is ErrNeedKeys,
// and an attempt that begins again succeeds.
var ErrKeysChanged error = keysChanged{}

type keysChanged struct{}

var keysChangedBusy = &domain.Error{Code: "temporarily_busy", Message: "The keys this needs changed while it was in progress. Try again."}

func (keysChanged) Error() string   { return keysChangedBusy.Message }
func (keysChanged) Unwrap() []error { return []error{keysChangedBusy, ErrNeedKeys} }

// ErrNoSealableKey is a holder whose only key generation may no longer be sealed under, until a
// new generation replaces it.
var ErrNoSealableKey = &domain.Error{Code: "temporarily_busy", Message: "New values cannot be saved until a new key replaces the current one. Try again later."}

// ErrKeyNotInBackup is a value a recovery cannot open because the restored database holds no key
// for its workspace: the backup was taken before the workspace's key existed, and what was sealed
// under that key opens only with it. It is a value the session cannot open, as cryptobox.ErrKey is.
var ErrKeyNotInBackup error = keyNotInBackup{}

type keyNotInBackup struct{}

func (keyNotInBackup) Error() string {
	return "this management database holds no key for the workspace: it was restored from a backup taken before the workspace's key existed, so it cannot open what the hosts sealed under that key; restore a backup taken after the workspace was created"
}
func (keyNotInBackup) Unwrap() error { return cryptobox.ErrKey }

// ErrKeyMissing is a recovery into a workspace with no key. Recovery runs with the installation
// offline, where nothing makes keys, so the operator makes them before recovering again.
var ErrKeyMissing = errors.New("the workspace has no key yet: run `astilba custody init-keys`, then recover again")

// ErrKeyPending is a holder still waiting for its first key, which the key job is making. How far
// that job has got across the installation is for operators, in its log and custody status.
var ErrKeyPending = &domain.Error{Code: "temporarily_busy", Message: "The key for saving this is being created. Try again shortly."}

// SessionFailure reports an error that came from the session itself, not from the value: the
// session ended, or was never given the value. Callers pass it on rather than reporting a
// missing key or a damaged value.
func SessionFailure(err error) bool {
	return errors.Is(err, ErrNeedKeys) || errors.Is(err, ErrNotAllowed)
}

// Request asks for keys for one piece of work.
type Request struct {
	// Workspace is uuid.Nil only for the installation's own values.
	Workspace uuid.UUID
	Purpose   Purpose
	Principal Principal
	Operation *uuid.UUID
	// Items are the values the session may use, when they are known up front.
	Items []Item
	// Kinds are the kinds it may use otherwise, for work that reads whatever it finds.
	Kinds []Kind
	// Reason is recorded with the purpose; each purpose has a default.
	Reason string
	// NewWorkspace is a workspace the caller's transaction is about to create, so no one is
	// yet a member of it.
	NewWorkspace bool
	// Connection is a Git connection whose webhook asks, which has a budget of its own.
	Connection *uuid.UUID
}

// Queries is what the broker reads and writes, which *db.Queries provides on a pool.
type Queries interface {
	SecretAccessMember(ctx context.Context, arg db.SecretAccessMemberParams) (string, error)
	SecretAccessServer(ctx context.Context, arg db.SecretAccessServerParams) (bool, error)
	SecretAccessWorkspaceExists(ctx context.Context, id uuid.UUID) (bool, error)
	InsertSecretAccess(ctx context.Context, arg db.InsertSecretAccessParams) error
	InsertSecretAccessOutcome(ctx context.Context, arg db.InsertSecretAccessOutcomeParams) error
	AbandonSecretAccess(ctx context.Context, olderThanSeconds float64) (int64, error)
	WorkspaceKeyWrappings(ctx context.Context, arg db.WorkspaceKeyWrappingsParams) ([]db.WorkspaceKeyWrappingsRow, error)
	InstallationKeyWrappings(ctx context.Context, arg db.InstallationKeyWrappingsParams) ([]db.InstallationKeyWrappingsRow, error)
}

type Broker struct {
	q       Queries
	backend custody.Legacy
	budgets *budgets
	log     *slog.Logger
	now     func() time.Time
	// installation and pool are set by Install, which an operator's command needs first.
	installation uuid.UUID
	pool         Pool
	keysWanted   chan struct{}
}

// New takes queries that do not run in a caller's transaction: each record commits on its own.
func New(q Queries, backend custody.Legacy, log *slog.Logger) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{q: q, backend: backend, budgets: newBudgets(), log: log, now: time.Now, keysWanted: make(chan struct{}, 1)}
}

// KeysWanted signals each time a value is refused because its holder has no key yet, so the key
// job can run now rather than at its next turn.
func (b *Broker) KeysWanted() <-chan struct{} { return b.keysWanted }

func (b *Broker) wantKeys() {
	select {
	case b.keysWanted <- struct{}{}:
	default:
	}
}

// NewDetached is New for a broker whose installation's records are kept somewhere other than a
// database, as a unit test's are: q also stores its keys. It cannot make keys.
func NewDetached(q Queries, backend custody.Legacy, installation uuid.UUID, log *slog.Logger) *Broker {
	b := New(q, backend, log)
	b.installation = installation
	return b
}

// Begin checks the request, records it, and only then returns a session holding the keys: the
// installation keyring, and each live generation of the workspace's key, or of the installation
// key for the installation's own values. It must not run with a transaction open: it commits a
// record of its own, and with an external key service it waits on the network.
func (b *Broker) Begin(ctx context.Context, r Request) (*Session, error) {
	rule, err := b.admit(ctx, r)
	if err != nil {
		return nil, err
	}
	if !b.budgets.take(b.now(), budgetKeys(r)) {
		return nil, domain.RateLimited
	}
	held, err := b.wrappedKeys(ctx, r)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	if err = b.record(ctx, id, r, generationsOf(held)); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		// The record is written, so it ends now rather than being swept as abandoned later.
		b.outcome(ctx, id, Refused, nil)
		return nil, err
	}
	keys, sealing, requests, err := b.unwrap(ctx, held, r.Workspace, string(r.Purpose))
	if err != nil {
		b.outcome(ctx, id, Failed, requests)
		return nil, err
	}
	s := &Session{broker: b, id: id, request: r, rule: rule, deadline: b.now().Add(rule.limit), ring: b.backend.Keyring(), keys: keys, sealing: sealing, keyless: held.keyless, requests: requests}
	b.log.Info("secret access", "access", id, "workspace", r.Workspace, "purpose", r.Purpose, "principal", r.Principal.Kind, "items", len(r.Items), "kinds", len(r.Kinds), "backend", b.backend.ID())
	return s, nil
}

// admit refuses a request the purpose table does not allow, or whose principal does not belong
// to the workspace.
func (b *Broker) admit(ctx context.Context, r Request) (rule, error) {
	rule, ok := rules[r.Purpose]
	if !ok || !r.Principal.wellFormed() || !slices.Contains(rule.principals, r.Principal.Kind) || len(r.Items) == 0 && len(r.Kinds) == 0 {
		return rule, domain.Forbidden
	}
	installation := r.Workspace == uuid.Nil
	for _, item := range r.Items {
		// A row always has an ID by the time its value is sealed: a nil one is a caller that has
		// not chosen it yet, and would seal the value for the wrong row.
		if item.ID == uuid.Nil || item.identity(r.Workspace) == "" || !rule.covers(item.Kind) {
			return rule, domain.Forbidden
		}
		// A person opens only their own second factor.
		if installation && (r.Principal.Account == nil || item.ID != *r.Principal.Account) {
			return rule, domain.Forbidden
		}
	}
	for _, kind := range r.Kinds {
		if slices.Contains(installationKinds, kind) || installation || !rule.covers(kind) {
			return rule, domain.Forbidden
		}
	}
	if installation {
		return rule, nil
	}
	// A workspace the caller's transaction is about to create has no members yet, so it must
	// not exist: otherwise anyone could put records in another tenant's history.
	if r.NewWorkspace {
		if r.Principal.Kind != AccountPrincipal || rule.owner {
			return rule, domain.Forbidden
		}
		exists, err := b.q.SecretAccessWorkspaceExists(ctx, r.Workspace)
		if err == nil && exists {
			return rule, domain.Conflict
		}
		return rule, err
	}
	switch r.Principal.Kind {
	case AccountPrincipal, AutomationPrincipal:
		if r.Principal.Kind == AutomationPrincipal && r.Principal.automationWorkspace != r.Workspace {
			return rule, domain.Forbidden
		}
		role, err := b.q.SecretAccessMember(ctx, db.SecretAccessMemberParams{WorkspaceID: r.Workspace, AccountID: *r.Principal.Account})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && rule.owner && role != "owner" {
			return rule, domain.Forbidden
		}
		return rule, err
	case AgentPrincipal:
		ok, err := b.q.SecretAccessServer(ctx, db.SecretAccessServerParams{WorkspaceID: r.Workspace, ServerID: *r.Principal.Server})
		if err == nil && !ok {
			return rule, domain.Forbidden
		}
		return rule, err
	}
	// The installation's own jobs and its operators act for every workspace.
	return rule, nil
}

func (r rule) covers(kind Kind) bool {
	return slices.Contains(r.open, kind) || slices.Contains(r.seal, kind)
}

// maxRecordedItems bounds a record. Beyond it the record keeps the count, and the kinds.
const maxRecordedItems = 256

// record commits the request's record, naming the key generations its session holds.
func (b *Broker) record(ctx context.Context, id uuid.UUID, r Request, held []int64) error {
	type recorded struct {
		Kind Kind       `json:"kind"`
		ID   *uuid.UUID `json:"id,omitempty"`
	}
	items := []recorded{}
	for _, item := range r.Items[:min(len(r.Items), maxRecordedItems)] {
		items = append(items, recorded{item.Kind, &item.ID})
	}
	for _, kind := range r.Kinds {
		items = append(items, recorded{Kind: kind})
	}
	rawItems, err := stablejson.Marshal(items)
	if err != nil {
		return err
	}
	generations, err := stablejson.Marshal(held)
	if err != nil {
		return err
	}
	reason := r.Reason
	if reason == "" {
		reason = defaultReasons[r.Purpose]
	}
	p := r.Principal
	var workspace *uuid.UUID
	if r.Workspace != uuid.Nil {
		workspace = &r.Workspace
	}
	// A request cancelled now must not give out keys with no record: the insert runs to its end
	// either way, and Begin refuses after it.
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = b.q.InsertSecretAccess(write, db.InsertSecretAccessParams{
		ID: id, WorkspaceID: workspace, Purpose: string(r.Purpose), PrincipalKind: string(p.Kind),
		AccountID: p.Account, AutomationID: p.Automation, ServerID: p.Server, AgentInstance: optionalText(p.Instance),
		Job: optionalText(p.Job), OperatorRef: optionalText(p.Operator), OperationID: r.Operation,
		Items: rawItems, ItemCount: int32(len(r.Items) + len(r.Kinds)), Generations: generations,
		BackendID: b.backend.ID(), Reason: reason,
	})
	return err
}

// optionalText is an empty string as NULL.
func optionalText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// outcome records how an access ended, with the key service's IDs for the requests its keys took,
// so each can be found in the provider's own audit log.
func (b *Broker) outcome(ctx context.Context, id uuid.UUID, outcome Outcome, requests []string) error {
	if requests == nil {
		requests = []string{}
	}
	ids, err := stablejson.Marshal(requests)
	if err != nil {
		return err
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return b.q.InsertSecretAccessOutcome(write, db.InsertSecretAccessOutcomeParams{AccessID: id, Outcome: string(outcome), ProviderRequestIds: ids})
}

// Sweep records as abandoned every access whose session must have ended without saying how.
func (b *Broker) Sweep(ctx context.Context) error {
	n, err := b.q.AbandonSecretAccess(ctx, (batchLimit + time.Minute).Seconds())
	if n > 0 {
		b.log.Warn("secret access abandoned", "records", n)
	}
	return err
}
