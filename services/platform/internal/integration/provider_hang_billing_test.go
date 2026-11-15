package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// hold makes the requests held matches wait until release, which the test's cleanup also
// calls. With once, only the first does.
func (s *stripeFixture) hold(t *testing.T, held func(*http.Request) bool, once bool) (<-chan struct{}, func()) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 64)
	s.mu.Lock()
	s.held, s.once, s.gate, s.entered = held, once, gate, entered
	s.mu.Unlock()
	release := sync.OnceFunc(func() {
		s.mu.Lock()
		if s.gate == gate {
			s.gate = nil
		}
		s.mu.Unlock()
		close(gate)
	})
	t.Cleanup(release)
	return entered, release
}

func subscriptionLookup(r *http.Request) bool {
	return r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/subscriptions/")
}

// subscribedWorkspace is a hosted workspace that completed its checkout for sub_fixture.
func subscribedWorkspace(t *testing.T) (*fixture, store.Principal, uuid.UUID, *stripeFixture, *billing.Service) {
	t.Helper()
	f, p, ws := hostedFixture(t)
	stripe := &stripeFixture{t: t, customer: "cus_fixture", sessions: map[string]hosting.Checkout{}, keys: map[string]bool{}, until: time.Now().Add(time.Hour), status: "active"}
	svc := &billing.Service{Store: f.st, Stripe: hosting.Stripe{Key: "fixture", Price: "price_monthly", HTTP: &http.Client{Transport: stripe}}, Origin: "https://panel.example.test", Now: time.Now}
	if _, e := svc.Checkout(t.Context(), p, ws); e != nil {
		t.Fatal(e)
	}
	stripe.complete()
	raw, sig := signedEvent(t, "checkout.session.completed", "evt_subscribed", onlySession(stripe))
	if e := svc.Webhook(t.Context(), raw, sig); e != nil {
		t.Fatal(e)
	}
	return f, p, ws, stripe, svc
}

func onlySession(s *stripeFixture) hosting.Checkout {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.sessions {
		return v
	}
	return hosting.Checkout{}
}

func (s *stripeFixture) subscriptionEvent(t *testing.T, id string) ([]byte, string) {
	return signedEvent(t, "customer.subscription.updated", id, map[string]any{"id": "sub_fixture", "customer": s.customer})
}

// requireWorkspaceWritable shows, while Stripe hangs, that nothing holds the workspace's lock,
// its queue or a transaction, and that an owner's billing write goes through.
func requireWorkspaceWritable(t *testing.T, f *fixture, p store.Principal, ws uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, done, err := f.st.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		t.Fatal("the workspace queue is held across a Stripe call:", err)
	}
	done()
	requireNoOpenTransaction(t, ctx, f)
	if err = f.st.InWorkspaceBilling(ctx, p, ws, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
		_, err := tx.Exec(ctx, "UPDATE hosted_subscriptions SET updated_at=now() WHERE workspace_id=$1", ws)
		return err
	}); err != nil {
		t.Fatal("an owner's billing write waited for Stripe:", err)
	}
}

// A burst of webhooks waiting on Stripe must leave the pool to every other tenant and the
// workspace's own writes, then apply once Stripe answers.
func TestHungStripeWebhooksHoldNothing(t *testing.T) {
	f, p, ws, stripe, svc := subscribedWorkspace(t)
	stripe.mu.Lock()
	lookups := stripe.lookups
	stripe.mu.Unlock()
	entered, release := stripe.hold(t, subscriptionLookup, false)
	size := int(f.st.Pool.Config().MaxConns) + 2
	results := make(chan error, size)
	for i := range size {
		raw, sig := stripe.subscriptionEvent(t, "evt_burst"+string(rune('a'+i)))
		go func() { results <- svc.Webhook(context.Background(), raw, sig) }()
	}
	for range size {
		waitEntered(t, entered)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// River keeps a listener and may be running a job, so leave it room.
	for range f.st.Pool.Config().MaxConns - 2 {
		conn, err := f.st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal("hung Stripe calls pinned the pool:", err)
		}
		defer conn.Release()
	}
	requireWorkspaceWritable(t, f, p, ws)
	release()
	for range size {
		if err := <-results; err != nil {
			t.Fatal("a webhook failed once Stripe answered:", err)
		}
	}
	var events int
	if err := f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM hosted_billing_events WHERE workspace_id=$1 AND id LIKE 'evt_burst%'", ws).Scan(&events); err != nil || events != size {
		t.Fatal("not every delivery was recorded", events, err)
	}
	// Answers overtaken by others were skipped, not asked for again: a burst cannot run a
	// delivery out of passes.
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	if stripe.lookups-lookups != size {
		t.Fatal("a delivery asked Stripe more than once", stripe.lookups-lookups, size)
	}
}

// Answers fetched with nothing held can arrive out of order. One asked for before another
// was must not replace it: here the earlier answer would restore lapsed access.
func TestOvertakenStripeAnswerIsNotApplied(t *testing.T) {
	f, p, ws, stripe, svc := subscribedWorkspace(t)
	entered, release := stripe.hold(t, subscriptionLookup, true)
	raw, sig := stripe.subscriptionEvent(t, "evt_earlier")
	earlier := make(chan error, 1)
	go func() { earlier <- svc.Webhook(context.Background(), raw, sig) }()
	waitEntered(t, entered)
	requireWorkspaceWritable(t, f, p, ws)
	stripe.mu.Lock()
	stripe.until = time.Now().Add(-time.Minute)
	stripe.mu.Unlock()
	raw, sig = stripe.subscriptionEvent(t, "evt_later")
	if err := svc.Webhook(t.Context(), raw, sig); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-earlier; err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(t.Context(), p, ws)
	if err != nil || status.ManagementUntil == nil || status.ManagementUntil.After(time.Now()) {
		t.Fatalf("an overtaken answer restored access: %+v %v", status, err)
	}
	var recorded bool
	if err = f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM hosted_billing_events WHERE id='evt_earlier')").Scan(&recorded); err != nil || !recorded {
		t.Fatal("the overtaken delivery was not recorded, so Stripe would send it forever", err)
	}
	// A later delivery asks again and applies what Stripe says now.
	stripe.mu.Lock()
	stripe.until = time.Now().Add(time.Hour)
	stripe.mu.Unlock()
	raw, sig = stripe.subscriptionEvent(t, "evt_renewed")
	if err = svc.Webhook(t.Context(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if status, err = svc.Status(t.Context(), p, ws); err != nil || !status.ManagementActive {
		t.Fatalf("a later answer was not applied: %+v %v", status, err)
	}
}

// Checkout looks for a lost session with nothing held, then decides again under the lock: a
// webhook that completed the checkout meanwhile stands, and no second checkout is created.
func TestHungCheckoutRecoveryHoldsNothing(t *testing.T) {
	f, p, ws := hostedFixture(t)
	stripe := &stripeFixture{t: t, customer: "cus_fixture", sessions: map[string]hosting.Checkout{}, keys: map[string]bool{}, until: time.Now().Add(time.Hour), status: "active", loseResponse: true}
	svc := &billing.Service{Store: f.st, Stripe: hosting.Stripe{Key: "fixture", Price: "price_monthly", HTTP: &http.Client{Transport: stripe}}, Origin: "https://panel.example.test", Now: time.Now}
	if _, e := svc.Checkout(t.Context(), p, ws); e == nil {
		t.Fatal("expected lost response")
	}
	stripe.complete()
	if _, e := f.st.Pool.Exec(t.Context(), "UPDATE hosted_checkouts SET expires_at=now()-interval '1 second' WHERE workspace_id=$1", ws); e != nil {
		t.Fatal(e)
	}
	entered, release := stripe.hold(t, func(r *http.Request) bool { return r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions" }, true)
	recovered := make(chan error, 1)
	go func() {
		_, e := svc.Checkout(context.Background(), p, ws)
		recovered <- e
	}()
	waitEntered(t, entered)
	requireWorkspaceWritable(t, f, p, ws)
	raw, sig := signedEvent(t, "checkout.session.completed", "evt_completed", onlySession(stripe))
	if e := svc.Webhook(t.Context(), raw, sig); e != nil {
		t.Fatal(e)
	}
	release()
	if e := <-recovered; !errors.Is(e, domain.Conflict) {
		t.Fatal("a completed checkout should direct the owner to the billing portal", e)
	}
	status, e := svc.Status(t.Context(), p, ws)
	if e != nil || !status.ManagementActive {
		t.Fatalf("completion not kept: %+v %v", status, e)
	}
	if len(stripe.keys) != 1 {
		t.Fatal("replacement checkout created after the webhook completed it")
	}
}

// Checkout asks again when what it fetched was overtaken, since that may have completed its
// intent. If every answer is overtaken it gives up with domain.Conflict after the pass limit,
// having held nothing while it asked.
func TestCheckoutGivesUpWhenEveryAnswerIsOvertaken(t *testing.T) {
	f, p, ws, stripe, svc := subscribedWorkspace(t)
	stripe.workspace = ws.String()
	// The subscription is cancelled, and the owner starts a checkout whose create response is lost.
	stripe.mu.Lock()
	stripe.status = "canceled"
	stripe.mu.Unlock()
	raw, sig := stripe.subscriptionEvent(t, "evt_cancelled")
	if e := svc.Webhook(t.Context(), raw, sig); e != nil {
		t.Fatal(e)
	}
	stripe.mu.Lock()
	stripe.loseResponse = true
	stripe.mu.Unlock()
	if _, e := svc.Checkout(t.Context(), p, ws); e == nil {
		t.Fatal("expected lost response")
	}
	stripe.complete()
	if _, e := f.st.Pool.Exec(t.Context(), "UPDATE hosted_checkouts SET expires_at=now()-interval '1 second' WHERE workspace_id=$1", ws); e != nil {
		t.Fatal(e)
	}
	listing := func(r *http.Request) bool { return r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions" }
	entered, release := stripe.hold(t, listing, true)
	result := make(chan error, 1)
	go func() {
		_, e := svc.Checkout(context.Background(), p, ws)
		result <- e
	}()
	// Each time Checkout asks, a webhook asked for later is applied before it decides.
	for i := range 3 {
		waitEntered(t, entered)
		requireWorkspaceWritable(t, f, p, ws)
		raw, sig := stripe.subscriptionEvent(t, "evt_overtakes"+string(rune('a'+i)))
		if e := svc.Webhook(t.Context(), raw, sig); e != nil {
			t.Fatal(e)
		}
		next, nextRelease := stripe.hold(t, listing, true)
		release()
		entered, release = next, nextRelease
	}
	select {
	case e := <-result:
		if !errors.Is(e, domain.Conflict) {
			t.Fatal("expected the pass limit's conflict", e)
		}
	case <-entered:
		t.Fatal("checkout asked more often than its pass limit allows")
	case <-time.After(15 * time.Second):
		t.Fatal("checkout never gave up")
	}
	var completed bool
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT completed FROM hosted_checkouts WHERE workspace_id=$1", ws).Scan(&completed); e != nil || completed {
		t.Fatal("a checkout that gave up recorded a completion", completed, e)
	}
}
