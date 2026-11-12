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
}

type Broker struct {
	q       Queries
	backend custody.Legacy
	budgets *budgets
	log     *slog.Logger
	now     func() time.Time
}

// New takes queries that do not run in a caller's transaction: each record commits on its own.
func New(q Queries, backend custody.Legacy, log *slog.Logger) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{q: q, backend: backend, budgets: newBudgets(), log: log, now: time.Now}
}

// Begin checks the request, records it, and only then returns a session holding the keys.
// It must not run with a transaction open: it commits a record of its own, and with an external
// key service it waits on the network.
func (b *Broker) Begin(ctx context.Context, r Request) (*Session, error) {
	rule, err := b.admit(ctx, r)
	if err != nil {
		return nil, err
	}
	if !b.budgets.take(budgetKey(r)) {
		return nil, domain.RateLimited
	}
	id := uuid.New()
	if err = b.record(ctx, id, r); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		// The record is written, so it ends now rather than being swept as abandoned later.
		b.outcome(ctx, id, Refused)
		return nil, err
	}
	s := &Session{broker: b, id: id, request: r, rule: rule, deadline: b.now().Add(rule.limit), ring: b.backend.Keyring()}
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
		if item.identity(r.Workspace) == "" || !rule.covers(item.Kind) {
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

func (b *Broker) record(ctx context.Context, id uuid.UUID, r Request) error {
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
	// Before per-workspace keys exist every value is under the installation keyring, which
	// counts as generation 0.
	generations, err := stablejson.Marshal([]int64{0})
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
	optional := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
	// A request cancelled now must not give out keys with no record: the insert runs to its end
	// either way, and Begin refuses after it.
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = b.q.InsertSecretAccess(write, db.InsertSecretAccessParams{
		ID: id, WorkspaceID: workspace, Purpose: string(r.Purpose), PrincipalKind: string(p.Kind),
		AccountID: p.Account, AutomationID: p.Automation, ServerID: p.Server, AgentInstance: optional(p.Instance),
		Job: optional(p.Job), OperatorRef: optional(p.Operator), OperationID: r.Operation,
		Items: rawItems, ItemCount: int32(len(r.Items) + len(r.Kinds)), Generations: generations,
		BackendID: b.backend.ID(), Reason: reason,
	})
	return err
}

func (b *Broker) outcome(ctx context.Context, id uuid.UUID, outcome Outcome) error {
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return b.q.InsertSecretAccessOutcome(write, db.InsertSecretAccessOutcomeParams{AccessID: id, Outcome: string(outcome), ProviderRequestIds: []byte("[]")})
}

// Sweep records as abandoned every access whose session must have ended without saying how.
func (b *Broker) Sweep(ctx context.Context) error {
	n, err := b.q.AbandonSecretAccess(ctx, (batchLimit + time.Minute).Seconds())
	if n > 0 {
		b.log.Warn("secret access abandoned", "records", n)
	}
	return err
}
