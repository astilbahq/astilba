package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// event signs a webhook of kind about sub_fixture or one of its invoices. Stripe identifiers
// are alphanumeric after their prefix, so id loses its underscores.
func (s *stripeFixture) event(t *testing.T, kind, id string) ([]byte, string) {
	t.Helper()
	id = strings.ReplaceAll(id, "_", "")
	object := map[string]any{"id": "sub_fixture", "customer": s.customer}
	if kind == "invoice.paid" {
		object = map[string]any{"id": "in_" + id, "customer": s.customer, "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_fixture"}}}
	}
	return signedEvent(t, kind, "evt_"+id, object)
}

// deliver sends the webhook event makes.
func (s *stripeFixture) deliver(t *testing.T, svc *billing.Service, kind, id string) {
	t.Helper()
	raw, sig := s.event(t, kind, id)
	if e := svc.Webhook(t.Context(), raw, sig); e != nil {
		t.Fatalf("%s %s: %v", kind, id, e)
	}
}

// requireManagementUntil checks the stored end of management exactly; nil means none.
func requireManagementUntil(t *testing.T, f *fixture, ws uuid.UUID, want *time.Time) {
	t.Helper()
	var got *time.Time
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT management_until FROM hosted_subscriptions WHERE workspace_id=$1", ws).Scan(&got); e != nil {
		t.Fatal(e)
	}
	if (got == nil) != (want == nil) || got != nil && !got.Equal(*want) {
		t.Fatalf("management until %v, want %v", got, want)
	}
}

func allowed(paid time.Time) *time.Time {
	end := paid.Add(hosting.RenewalAllowance)
	return &end
}

// Stripe advances the period when it creates the renewal invoice, before charging it. Only the
// payment extends management, and a repeated delivery changes nothing.
func TestRenewalExtendsManagementOnlyOncePaid(t *testing.T) {
	f, _, ws, stripe, svc := subscribedWorkspace(t)
	first := stripe.periodEnd
	requireManagementUntil(t, f, ws, allowed(first))
	stripe.renew("open")
	stripe.deliver(t, svc, "customer.subscription.updated", "renewal_created")
	requireManagementUntil(t, f, ws, allowed(first))
	stripe.settle("active", "paid")
	stripe.deliver(t, svc, "invoice.paid", "renewal_paid")
	second := stripe.periodEnd
	requireManagementUntil(t, f, ws, allowed(second))
	stripe.deliver(t, svc, "invoice.paid", "renewal_paid")
	requireManagementUntil(t, f, ws, allowed(second))
}

// A renewal whose payment fails keeps the period already paid for, through dunning, and
// nothing more. Paying later extends it.
func TestFailedRenewalKeepsOnlyThePaidPeriod(t *testing.T) {
	f, p, ws, stripe, svc := subscribedWorkspace(t)
	first := stripe.periodEnd
	stripe.renew("open")
	stripe.deliver(t, svc, "customer.subscription.updated", "renewal_created")
	stripe.settle("past_due", "open")
	stripe.deliver(t, svc, "customer.subscription.updated", "past_due")
	requireManagementUntil(t, f, ws, allowed(first))
	if status, e := svc.Status(t.Context(), p, ws); e != nil || status.Status != "past_due" {
		t.Fatalf("status not kept current: %+v %v", status, e)
	}
	stripe.settle("active", "paid")
	stripe.deliver(t, svc, "invoice.paid", "retry_paid")
	requireManagementUntil(t, f, ws, allowed(stripe.periodEnd))
}

// While Stripe still reports a renewal as active but has not been paid, management lasts
// RenewalAllowance past the paid period and not a moment longer.
func TestRenewalAllowanceBoundary(t *testing.T) {
	for _, c := range []struct {
		name   string
		margin time.Duration
		active bool
	}{{"inside", 2 * time.Minute, true}, {"outside", -2 * time.Minute, false}} {
		t.Run(c.name, func(t *testing.T) {
			stripe := paidUntilFixture(t, time.Now().Add(-hosting.RenewalAllowance+c.margin))
			paid := stripe.periodEnd
			stripe.renew("open")
			f, p, ws, _, _ := subscribedWorkspaceWith(t, stripe)
			requireManagementUntil(t, f, ws, allowed(paid))
			_, e := f.execution.CreateServer(t.Context(), p, ws, api.CreateServer{Name: "Renewing", Role: "deployment"})
			if c.active && e != nil {
				t.Fatal("management ended inside the renewal allowance:", e)
			}
			if !c.active && e != store.SubscriptionRequired {
				t.Fatal("management outlasted the renewal allowance:", e)
			}
		})
	}
}

// Paused collection leaves the subscription active and advances its period, but the renewal
// invoice is voided, marked uncollectible or kept as a draft, so nothing is paid.
func TestPausedCollectionDoesNotExtendManagement(t *testing.T) {
	for behavior, invoice := range map[string]string{"void": "void", "mark_uncollectible": "uncollectible", "keep_as_draft": "draft"} {
		t.Run(behavior, func(t *testing.T) {
			f, p, ws, stripe, svc := subscribedWorkspace(t)
			first := stripe.periodEnd
			stripe.renew(invoice)
			stripe.deliver(t, svc, "customer.subscription.updated", "paused")
			requireManagementUntil(t, f, ws, allowed(first))
			svc.Now = func() time.Time { return first.Add(hosting.RenewalAllowance + time.Second) }
			if status, e := svc.Status(t.Context(), p, ws); e != nil || status.Status != "active" || status.ManagementActive {
				t.Fatalf("paused collection kept management: %+v %v", status, e)
			}
		})
	}
}

// Cancelling ends management when the subscription ended, however the paid renewal before it
// was delivered: observed while the subscription was active, only after it was cancelled, or
// not before the checkout itself was applied. Repeats change nothing.
func TestCancellationEndsManagementWhateverTheDeliveryOrder(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("renewal_seen_after_cancel=%v", late), func(t *testing.T) {
			// The paid period ended three days ago; the renewal is paid; the customer
			// cancels now, inside the renewed period.
			f, _, ws, stripe, svc := subscribedWorkspaceWith(t, paidUntilFixture(t, time.Now().Add(-72*time.Hour)))
			stripe.renew("paid")
			if !late {
				stripe.deliver(t, svc, "invoice.paid", "renewal_paid")
			}
			ended := time.Now()
			stripe.cancel(ended)
			stripe.deliver(t, svc, "customer.subscription.deleted", "cancelled")
			stripe.deliver(t, svc, "invoice.paid", "renewal_paid")
			stripe.deliver(t, svc, "customer.subscription.deleted", "cancelled")
			stripe.deliver(t, svc, "invoice.paid", "renewal_paid")
			requireManagementUntil(t, f, ws, stripe.endedAt)
		})
	}
	t.Run("checkout_applied_after_cancel", func(t *testing.T) {
		stripe := newStripeFixture(t)
		stripe.cancel(time.Now())
		f, _, ws, _, _ := subscribedWorkspaceWith(t, stripe)
		requireManagementUntil(t, f, ws, stripe.endedAt)
	})
}

// A checkout pass can observe Stripe before the first invoice is paid and apply after the
// invoice.paid event arrives. The event is refused rather than consumed, so Stripe's
// redelivery, after the checkout is applied, grants management.
func TestInvoicePaidBeforeCheckoutIsAppliedIsRedelivered(t *testing.T) {
	f, p, ws := hostedFixture(t)
	stripe := newStripeFixture(t)
	stripe.settle("incomplete", "open")
	svc := &billing.Service{Store: f.st, Stripe: hosting.Stripe{Key: "fixture", Price: "price_monthly", HTTP: &http.Client{Transport: stripe}}, Origin: "https://panel.example.test", Now: time.Now}
	if _, e := svc.Checkout(t.Context(), p, ws); e != nil {
		t.Fatal(e)
	}
	stripe.complete()
	entered, release := stripe.hold(t, func(r *http.Request) bool { return r.Method == "GET" && r.URL.Path == "/v1/invoices" }, true)
	raw, sig := signedEvent(t, "checkout.session.completed", "evt_completed", onlySession(stripe))
	completed := make(chan error, 1)
	go func() { completed <- svc.Webhook(context.Background(), raw, sig) }()
	waitEntered(t, entered)
	stripe.settle("active", "paid")
	paid, paidSig := stripe.event(t, "invoice.paid", "first_paid")
	if e := svc.Webhook(t.Context(), paid, paidSig); !errors.Is(e, domain.Conflict) {
		t.Fatal("an invoice event before its subscription was held was not refused for redelivery:", e)
	}
	release()
	if e := <-completed; e != nil {
		t.Fatal(e)
	}
	requireManagementUntil(t, f, ws, nil)
	var recorded bool
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM hosted_billing_events WHERE id='evt_firstpaid')").Scan(&recorded); e != nil || recorded {
		t.Fatal("the refused delivery was recorded, so its redelivery would be skipped", e)
	}
	if e := svc.Webhook(t.Context(), paid, paidSig); e != nil {
		t.Fatal(e)
	}
	requireManagementUntil(t, f, ws, allowed(stripe.periodEnd))
}

// A checkout can complete before its first invoice is paid, as with a delayed payment method.
// Management starts only when it is.
func TestUnpaidCheckoutGrantsNothingUntilPaid(t *testing.T) {
	stripe := newStripeFixture(t)
	stripe.settle("incomplete", "open")
	f, _, ws, _, svc := subscribedWorkspaceWith(t, stripe)
	requireManagementUntil(t, f, ws, nil)
	stripe.settle("active", "paid")
	stripe.deliver(t, svc, "invoice.paid", "first_paid")
	requireManagementUntil(t, f, ws, allowed(stripe.periodEnd))
	// An invoice event for a customer no workspace holds is ignored.
	raw, sig := signedEvent(t, "invoice.paid", "evt_stranger", map[string]any{"id": "in_stranger", "customer": "cus_stranger"})
	if e := svc.Webhook(t.Context(), raw, sig); e != nil {
		t.Fatal(e)
	}
}
