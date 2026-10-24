package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	Store  *store.Store
	Stripe hosting.Stripe
	Origin string
	Now    func() time.Time
}
type Status struct {
	Hosted          bool       `json:"hosted"`
	BillingEnabled  bool       `json:"billingEnabled"`
	Status          string     `json:"status"`
	ManagementUntil *time.Time `json:"managementUntil,omitempty"`
	RecoveryUntil   *time.Time `json:"recoveryUntil,omitempty"`
	ServerLimit     int        `json:"serverLimit"`
	Support         string     `json:"support"`
}

func (s *Service) Status(ctx context.Context, p store.Principal, ws uuid.UUID) (Status, error) {
	out := Status{Hosted: s.Store.Hosted, BillingEnabled: s.Store.Hosting.BillingEnabled(), Status: "self_hosted", Support: s.Store.Hosting.Support}
	err := s.Store.InWorkspaceTx(ctx, p, ws, false, false, false, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		if !s.Store.Hosted {
			return nil
		}
		e, err := store.Entitlement(ctx, tx, ws)
		out.Status = e.Status
		out.ManagementUntil = e.Until
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

func (s *Service) Checkout(ctx context.Context, p store.Principal, ws uuid.UUID) (string, error) {
	if !s.Store.Hosted || !s.Store.Hosting.BillingEnabled() {
		return "", domain.Forbidden
	}
	var current intent
	var customer *string
	e := s.Store.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
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
			old, e := s.Stripe.FindCheckout(ctx, *customer, current.ID.String())
			if e != nil {
				return e
			}
			if old != nil {
				if old.Status == "complete" {
					if e = s.applyCheckout(ctx, tx, ws, *old); e != nil {
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
	result, e := s.Stripe.Checkout(ctx, current.ID.String(), *customer, s.Origin+"/members?workspace="+ws.String(), current.Expires)
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
func (s *Service) applySubscription(ctx context.Context, tx pgx.Tx, ws uuid.UUID, id, customer string, allowNew bool) error {
	sub, e := s.Stripe.Subscription(ctx, id, customer)
	if e != nil {
		return e
	}
	var previous *time.Time
	var known *string
	var previousStatus string
	if e = tx.QueryRow(ctx, "SELECT management_until,subscription_id,status FROM hosted_subscriptions WHERE workspace_id=$1", ws).Scan(&previous, &known, &previousStatus); e != nil {
		return e
	}
	if !allowNew && (known == nil || *known != id) {
		return nil
	}
	if allowNew && known != nil && *known != id && previousStatus != "canceled" && previousStatus != "incomplete_expired" {
		return domain.Conflict
	}
	until := previous
	if sub.Status == "active" {
		until = sub.Entitlement().Until
	}
	if sub.EndedAt != nil {
		end := time.Unix(*sub.EndedAt, 0)
		if until != nil && end.Before(*until) {
			until = &end
		}
	}
	_, e = tx.Exec(ctx, `UPDATE hosted_subscriptions SET subscription_id=$2,status=$3,management_until=$4,updated_at=now() WHERE workspace_id=$1 AND customer_id=$5`, ws, id, sub.Status, until, customer)
	return e
}
func (s *Service) applyCheckout(ctx context.Context, tx pgx.Tx, ws uuid.UUID, session hosting.Checkout) error {
	var id uuid.UUID
	var customer string
	var stored *string
	if e := tx.QueryRow(ctx, `SELECT c.intent_id,s.customer_id,c.session_id FROM hosted_checkouts c JOIN hosted_subscriptions s USING(workspace_id) WHERE c.workspace_id=$1`, ws).Scan(&id, &customer, &stored); e != nil {
		return e
	}
	if session.Reference != id.String() || session.Customer != customer || session.Status != "complete" || (stored != nil && *stored != session.ID) {
		return domain.Conflict
	}
	if e := s.applySubscription(ctx, tx, ws, session.Subscription, customer, true); e != nil {
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
	if event.Type != "checkout.session.completed" && event.Type != "customer.subscription.updated" && event.Type != "customer.subscription.deleted" {
		return nil
	}
	var obj struct {
		ID, Customer string
		Reference    string `json:"client_reference_id"`
	}
	if json.Unmarshal(event.Data.Object, &obj) != nil {
		return domain.Invalid("event")
	}
	var ws uuid.UUID
	if event.Type == "checkout.session.completed" {
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
	} else {
		e = s.Store.Pool.QueryRow(ctx, "SELECT workspace_id FROM hosted_subscriptions WHERE customer_id=$1 AND subscription_id=$2", obj.Customer, obj.ID).Scan(&ws)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
		return e
	}
	n, e := tx.Exec(ctx, "INSERT INTO hosted_billing_events(id,workspace_id) VALUES($1,$2) ON CONFLICT DO NOTHING", event.ID, ws)
	if e != nil {
		return e
	}
	if n.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if event.Type == "checkout.session.completed" {
		session, err := s.Stripe.GetCheckout(ctx, obj.ID)
		if err != nil {
			return err
		}
		e = s.applyCheckout(ctx, tx, ws, session)
	} else {
		e = s.applySubscription(ctx, tx, ws, obj.ID, obj.Customer, false)
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
