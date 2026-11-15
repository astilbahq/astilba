package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// deliver sends a signed webhook of kind about sub_fixture or one of its invoices. Stripe
// identifiers are alphanumeric after their prefix, so id loses its underscores.
func (s *stripeFixture) deliver(t *testing.T, svc *billing.Service, kind, id string) {
	t.Helper()
	id = strings.ReplaceAll(id, "_", "")
	object := map[string]any{"id": "sub_fixture", "customer": s.customer}
	if kind == "invoice.paid" || kind == "invoice.payment_failed" {
		object = map[string]any{"id": "in_" + id, "customer": s.customer, "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_fixture"}}}
	}
	raw, sig := signedEvent(t, kind, "evt_"+id, object)
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
	stripe.deliver(t, svc, "invoice.payment_failed", "renewal_failed")
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
			stripe.deliver(t, svc, "invoice.payment_failed", "paused_invoice")
			requireManagementUntil(t, f, ws, allowed(first))
			svc.Now = func() time.Time { return first.Add(hosting.RenewalAllowance + time.Second) }
			if status, e := svc.Status(t.Context(), p, ws); e != nil || status.Status != "active" || status.ManagementActive {
				t.Fatalf("paused collection kept management: %+v %v", status, e)
			}
		})
	}
}

// Each event only prompts a fresh look at Stripe, so neither the order of deliveries nor
// repeats change the result, and an invoice.paid event grants nothing on its own.
func TestBillingEventOrderDoesNotChangeManagement(t *testing.T) {
	events := []string{"customer.subscription.updated", "invoice.paid", "invoice.payment_failed"}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, paid := range []bool{true, false} {
		for _, order := range orders {
			t.Run(fmt.Sprintf("paid=%v/%v", paid, order), func(t *testing.T) {
				f, _, ws, stripe, svc := subscribedWorkspace(t)
				first := stripe.periodEnd
				stripe.renew("open")
				want := allowed(first)
				if paid {
					stripe.settle("active", "paid")
					want = allowed(stripe.periodEnd)
				} else {
					stripe.settle("past_due", "open")
				}
				for _, i := range order {
					stripe.deliver(t, svc, events[i], fmt.Sprint(i))
					stripe.deliver(t, svc, events[i], fmt.Sprint(i))
				}
				requireManagementUntil(t, f, ws, want)
			})
		}
	}
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
