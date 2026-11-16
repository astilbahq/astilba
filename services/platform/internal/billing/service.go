package billing

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Service struct {
	Store  *store.Store
	Stripe hosting.Stripe
	Origin string
	Now    func() time.Time
}
type Status struct {
	Hosted           bool       `json:"hosted"`
	BillingEnabled   bool       `json:"billingEnabled"`
	Status           string     `json:"status"`
	ManagementUntil  *time.Time `json:"managementUntil,omitempty"`
	RecoveryUntil    *time.Time `json:"recoveryUntil,omitempty"`
	ServerLimit      int        `json:"serverLimit"`
	Support          string     `json:"support"`
	ManagementActive bool       `json:"managementActive"`
}

func (s *Service) Status(ctx context.Context, p store.Principal, ws uuid.UUID) (Status, error) {
	out := Status{Hosted: s.Store.Hosted, BillingEnabled: s.Store.Hosting.BillingEnabled(), Status: "self_hosted", Support: s.Store.Hosting.Support}
	err := s.Store.InWorkspaceTx(ctx, p, ws, store.WorkspaceAccess{}, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		if !s.Store.Hosted {
			return nil
		}
		e, err := store.Entitlement(ctx, tx, ws)
		out.Status = e.Status
		out.ManagementUntil = e.Until
		out.ManagementActive = e.Active(s.Now())
		out.ServerLimit = e.ServerLimit
		if e.Until != nil {
			recovery := e.Until.Add(hosting.GracePeriod)
			out.RecoveryUntil = &recovery
		}
		return err
	})
	return out, err
}
func (s *Service) Portal(ctx context.Context, p store.Principal, ws uuid.UUID) (string, error) {
	if !s.Store.Hosted || !s.Store.Hosting.BillingEnabled() {
		return "", domain.Forbidden
	}
	var customer string
	e := s.Store.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		return tx.QueryRow(ctx, "SELECT customer_id FROM hosted_subscriptions WHERE workspace_id=$1 AND customer_id IS NOT NULL", ws).Scan(&customer)
	})
	if e != nil {
		return "", e
	}
	return s.Stripe.Portal(ctx, customer, s.Origin+"/members?workspace="+ws.String())
}

type intent struct {
	ID        uuid.UUID
	Expires   time.Time
	Session   *string
	URL       string
	Completed bool
}

// passLimit bounds how often a checkout asks Stripe again because the answer it had was
// overtaken. At the limit the caller gets domain.Conflict.
const passLimit = 4

// A question is what a pass under the workspace lock needs Stripe to answer. One field besides
// the customer is set: the checkout intent to find among the customer's sessions, the checkout
// session a webhook named, or the subscription a webhook named.
type question struct {
	customer, reference, session, subscription string
}

// An answer is Stripe's reply to a question, fetched with nothing held. checkout is nil when
// no session carries the reference. subscription is set when one was asked for or the
// checkout is complete.
type answer struct {
	question     question
	checkout     *hosting.Checkout
	subscription *observation
}

// An observation is what Stripe says of a subscription: everything applySubscription decides
// from, so that a decision under the lock asks nothing. Ticket orders observations by when they
// were asked for. PaidThrough is the end of the latest period a paid invoice covers, nil when
// none is paid.
type observation struct {
	Ticket       int64
	Subscription hosting.Subscription
	PaidThrough  *time.Time
}

// errSuperseded is an observation of the workspace's subscription older than the one it holds.
// That one was asked for later, so it saw every change this one could have.
var errSuperseded = errors.New("a later billing observation was applied")

// asking is returned by a pass that needs Stripe's answer. Its transaction rolls back, so the
// pass must have had no other effect.
type asking struct{ question question }

func (*asking) Error() string { return "billing provider answer needed" }

// decide runs pass until it stops asking, asking Stripe each question with no transaction or
// lock held. Every pass decides afresh under the workspace lock from what is stored then. A
// pass cannot ask after it commits.
func (s *Service) decide(ctx context.Context, pass func(seen *answer) error) error {
	var seen *answer
	for n := 1; ; n++ {
		err := pass(seen)
		var a *asking
		if !errors.As(err, &a) {
			return err
		}
		if n == passLimit {
			return domain.Conflict
		}
		got, err := s.ask(ctx, a.question)
		if err != nil {
			return err
		}
		seen = &got
	}
}

// answered returns the answer to q, or asks for it.
func answered(seen *answer, q question) (*answer, error) {
	if seen == nil || seen.question != q {
		return nil, &asking{q}
	}
	return seen, nil
}

func (s *Service) ask(ctx context.Context, q question) (answer, error) {
	a := answer{question: q}
	// The ticket is taken before Stripe is asked anything, so it orders the answers by the
	// earliest moment each could describe.
	ticket, e := db.New(s.Store.Pool).TakeObservationTicket(ctx)
	if e != nil {
		return a, e
	}
	switch {
	case q.reference != "":
		found, e := s.Stripe.FindCheckout(ctx, q.customer, q.reference)
		if e != nil {
			return a, e
		}
		a.checkout = found
	case q.session != "":
		found, e := s.Stripe.GetCheckout(ctx, q.session)
		if e != nil {
			return a, e
		}
		a.checkout = &found
	}
	id, customer := q.subscription, q.customer
	if a.checkout != nil && a.checkout.Status == "complete" {
		id, customer = a.checkout.Subscription, a.checkout.Customer
	}
	if id != "" {
		seen, e := s.observeSubscription(ctx, ticket, id, customer)
		if e != nil {
			return a, e
		}
		a.subscription = &seen
	}
	return a, nil
}

// observeSubscription asks Stripe about a subscription and its paid invoices, with nothing
// held. The two answers need not be one snapshot: the paid invoices only ever grow, and the
// ticket was taken before either was asked for.
func (s *Service) observeSubscription(ctx context.Context, ticket int64, id, customer string) (observation, error) {
	sub, e := s.Stripe.Subscription(ctx, id, customer)
	if e != nil {
		return observation{}, e
	}
	paid, e := s.Stripe.PaidThrough(ctx, id, customer)
	if e != nil {
		return observation{}, e
	}
	return observation{Ticket: ticket, Subscription: sub, PaidThrough: paid}, nil
}

func (s *Service) Checkout(ctx context.Context, p store.Principal, ws uuid.UUID) (string, error) {
	if !s.Store.Hosted || !s.Store.Hosting.BillingEnabled() {
		return "", domain.Forbidden
	}
	var current intent
	var customer *string
	e := s.decide(ctx, func(seen *answer) error {
		return s.Store.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
			current, customer = intent{}, nil
			var subscription *string
			var status string
			if e := tx.QueryRow(ctx, "SELECT customer_id,subscription_id,status FROM hosted_subscriptions WHERE workspace_id=$1", ws).Scan(&customer, &subscription, &status); e != nil {
				return e
			}
			if subscription != nil && status != "canceled" && status != "incomplete_expired" {
				return domain.Conflict
			}
			err := tx.QueryRow(ctx, "SELECT intent_id,expires_at,session_id,url,completed FROM hosted_checkouts WHERE workspace_id=$1", ws).Scan(&current.ID, &current.Expires, &current.Session, &current.URL, &current.Completed)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && !current.Completed && !s.Now().Before(current.Expires) && customer != nil {
				// A lost create response may have left a session for this intent at Stripe.
				q := question{customer: *customer, reference: current.ID.String()}
				a, e := answered(seen, q)
				if e != nil {
					return e
				}
				if old := a.checkout; old != nil {
					if old.Status == "complete" {
						e = s.applyCheckout(ctx, tx, ws, *old, a.subscription)
						if errors.Is(e, errSuperseded) {
							// What was applied since may have completed this intent: decide again.
							return &asking{q}
						}
						if e != nil {
							return e
						}
						current.Completed = true
						return nil
					}
					if old.Status != "expired" {
						return domain.Conflict
					}
				}
			}
			if err != nil || current.Completed || !s.Now().Before(current.Expires) {
				current = intent{ID: uuid.New(), Expires: s.Now().Truncate(time.Second).Add(time.Hour)}
				_, e := tx.Exec(ctx, `INSERT INTO hosted_checkouts(workspace_id,intent_id,expires_at) VALUES($1,$2,$3) ON CONFLICT(workspace_id) DO UPDATE SET intent_id=$2,expires_at=$3,session_id=NULL,url='',completed=false`, ws, current.ID, current.Expires)
				return e
			}
			return nil
		})
	})
	if e != nil {
		return "", e
	}
	if current.Completed {
		return "", domain.Conflict
	}
	if current.URL != "" {
		return current.URL, nil
	}
	// Reservation is committed before any provider effect. Response loss reuses
	// this exact intent and provider idempotency key.
	if customer == nil {
		id, e := s.Stripe.Customer(ctx, current.ID.String())
		if e != nil {
			return "", e
		}
		e = s.Store.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
			var active uuid.UUID
			if e := tx.QueryRow(ctx, "SELECT intent_id FROM hosted_checkouts WHERE workspace_id=$1", ws).Scan(&active); e != nil {
				return e
			}
			if active != current.ID {
				return domain.Conflict
			}
			_, e := tx.Exec(ctx, "UPDATE hosted_subscriptions SET customer_id=$2 WHERE workspace_id=$1 AND (customer_id IS NULL OR customer_id=$2)", ws, id)
			return e
		})
		if e != nil {
			return "", e
		}
		customer = &id
	}
	result, e := s.Stripe.Checkout(ctx, current.ID.String(), *customer, s.Origin+"/onboarding?workspace="+ws.String(), current.Expires)
	if e != nil {
		return "", e
	}
	e = s.Store.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		n, e := tx.Exec(ctx, "UPDATE hosted_checkouts SET session_id=$3,url=$4 WHERE workspace_id=$1 AND intent_id=$2 AND NOT completed", ws, current.ID, result.ID, result.URL)
		if e == nil && n.RowsAffected() != 1 {
			return domain.Conflict
		}
		return e
	})
	return result.URL, e
}

// applySubscription stores what Stripe said of subscription id, under the workspace lock. It
// decides only from seen and the stored row, and returns errSuperseded, writing nothing, when the
// row holds an observation asked for later.
func (s *Service) applySubscription(ctx context.Context, tx pgx.Tx, ws uuid.UUID, id, customer string, seen observation, allowNew bool) error {
	sub := seen.Subscription
	var previous *time.Time
	var known *string
	var previousStatus string
	var held int64
	if e := tx.QueryRow(ctx, "SELECT management_until,subscription_id,status,observed_ticket FROM hosted_subscriptions WHERE workspace_id=$1", ws).Scan(&previous, &known, &previousStatus, &held); e != nil {
		return e
	}
	if !allowNew && (known == nil || *known != id) {
		return nil
	}
	// Tickets order observations of one subscription. Whether one may replace another is up to allowNew.
	if known != nil && *known == id && seen.Ticket <= held {
		return errSuperseded
	}
	if allowNew && known != nil && *known != id && previousStatus != "canceled" && previousStatus != "incomplete_expired" {
		return domain.Conflict
	}
	until := extend(previous, seen.PaidThrough)
	if sub.EndedAt != nil {
		end := time.Unix(*sub.EndedAt, 0)
		if until != nil && end.Before(*until) {
			until = &end
		}
	}
	_, e := tx.Exec(ctx, `UPDATE hosted_subscriptions SET subscription_id=$2,status=$3,management_until=$4,updated_at=now(),observed_ticket=$6 WHERE workspace_id=$1 AND customer_id=$5`, ws, id, sub.Status, until, customer, seen.Ticket)
	return e
}

// extend returns how long management lasts given what was stored and a subscription's paid
// evidence. Only a paid invoice moves it later, to the end of the period paid for plus
// hosting.RenewalAllowance. Stripe's current period end is not evidence: it advances when the
// renewal invoice is created, before anything is paid, and stays advanced while collection is
// paused. Status is not consulted either: a period paid for stays paid for whatever the
// subscription became, and its ended_at, applied after this, is what ends it. So the result
// depends only on what Stripe holds, never on which observation was applied first: it is
// never earlier than stored, and the paid evidence only grows.
func extend(stored *time.Time, paid *time.Time) *time.Time {
	if paid == nil {
		return stored
	}
	end := paid.Add(hosting.RenewalAllowance)
	if stored != nil && !end.After(*stored) {
		return stored
	}
	return &end
}

// applyCheckout completes the workspace's checkout intent from a session Stripe reported
// complete, and seen, what Stripe said of the session's subscription.
func (s *Service) applyCheckout(ctx context.Context, tx pgx.Tx, ws uuid.UUID, session hosting.Checkout, seen *observation) error {
	var id uuid.UUID
	var customer string
	var stored *string
	if e := tx.QueryRow(ctx, `SELECT c.intent_id,s.customer_id,c.session_id FROM hosted_checkouts c JOIN hosted_subscriptions s USING(workspace_id) WHERE c.workspace_id=$1`, ws).Scan(&id, &customer, &stored); e != nil {
		return e
	}
	if session.Reference != id.String() || session.Customer != customer || session.Status != "complete" || (stored != nil && *stored != session.ID) || seen == nil {
		return domain.Conflict
	}
	if e := s.applySubscription(ctx, tx, ws, session.Subscription, customer, *seen, true); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE hosted_checkouts SET completed=true,session_id=$2 WHERE workspace_id=$1`, ws, session.ID)
	return e
}
func (s *Service) Webhook(ctx context.Context, raw []byte, signature string) error {
	if !s.Store.Hosted || !s.Store.Hosting.BillingEnabled() {
		return domain.NotFound
	}
	event, e := hosting.VerifyEvent(raw, signature, s.Store.Hosting.WebhookSecret, s.Now())
	if e != nil {
		return domain.Invalid("signature")
	}
	switch event.Type {
	case "checkout.session.completed", "customer.subscription.updated", "customer.subscription.deleted", "invoice.paid":
	default:
		return nil
	}
	var obj struct {
		ID        string `json:"id"`
		Customer  string `json:"customer"`
		Reference string `json:"client_reference_id"`
		Parent    struct {
			Details struct {
				Subscription string `json:"subscription"`
			} `json:"subscription_details"`
		} `json:"parent"`
	}
	if json.Unmarshal(event.Data.Object, &obj) != nil {
		return domain.Invalid("event")
	}
	var ws uuid.UUID
	checkout := event.Type == "checkout.session.completed"
	// An invoice event only says to look again: the workspace is found by its own customer,
	// and the subscription to observe is the one it holds.
	invoice := event.Type == "invoice.paid"
	switch {
	case invoice:
		ws, e = db.New(s.Store.Pool).HostedWorkspaceOfCustomer(ctx, pgtype.Text{String: obj.Customer, Valid: true})
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
	case checkout:
		id, e := uuid.Parse(obj.Reference)
		if e != nil {
			return domain.Invalid("event")
		}
		e = s.Store.Pool.QueryRow(ctx, `SELECT s.workspace_id FROM hosted_subscriptions s JOIN hosted_checkouts c USING(workspace_id) WHERE s.customer_id=$1 AND c.intent_id=$2`, obj.Customer, id).Scan(&ws)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
	default:
		e = s.Store.Pool.QueryRow(ctx, "SELECT workspace_id FROM hosted_subscriptions WHERE customer_id=$1 AND subscription_id=$2", obj.Customer, obj.ID).Scan(&ws)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
	}
	return s.decide(ctx, func(seen *answer) error {
		tx, done, e := s.Store.BeginWorkspaceWrite(ctx, ws)
		if e != nil {
			return e
		}
		defer done()
		// A delivery already applied commits nothing new and asks Stripe nothing.
		n, e := tx.Exec(ctx, "INSERT INTO hosted_billing_events(id,workspace_id) VALUES($1,$2) ON CONFLICT DO NOTHING", event.ID, ws)
		if e != nil {
			return e
		}
		if n.RowsAffected() == 0 {
			return tx.Commit(ctx)
		}
		subscription := obj.ID
		if invoice {
			held, e := db.New(tx).HeldSubscription(ctx, ws)
			if e != nil {
				return e
			}
			// The invoice's own subscription only says whether to wait, never what was paid.
			named := obj.Parent.Details.Subscription
			ended := held.Status == "canceled" || held.Status == "incomplete_expired"
			unheld := !held.SubscriptionID.Valid || ended && named != "" && named != held.SubscriptionID.String
			if unheld && held.CheckoutOpen {
				// The open checkout may have observed Stripe before this payment and not yet
				// be applied. Recording the event would lose it, so Stripe is asked to
				// deliver it again.
				return domain.Conflict
			}
			if !held.SubscriptionID.Valid {
				// No checkout can bring the invoice's subscription here.
				return tx.Commit(ctx)
			}
			subscription = held.SubscriptionID.String
		}
		q := question{customer: obj.Customer}
		if checkout {
			q.session = obj.ID
		} else {
			q.subscription = subscription
		}
		a, e := answered(seen, q)
		if e != nil {
			return e
		}
		if checkout {
			e = s.applyCheckout(ctx, tx, ws, *a.checkout, a.subscription)
		} else {
			e = s.applySubscription(ctx, tx, ws, subscription, obj.Customer, *a.subscription, false)
		}
		// An observation asked for after this one has been applied, and saw at least as much.
		if e != nil && !errors.Is(e, errSuperseded) {
			return e
		}
		return tx.Commit(ctx)
	})
}
