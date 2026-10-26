package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func hostedFixture(t *testing.T) (*fixture, store.Principal, uuid.UUID) {
	f := setup(t)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 5, ServerLimit: 2, PriceID: "price_monthly", SecretKey: "sk_test_fixture", WebhookSecret: "whsec_fixture"}
	ctx := context.Background()
	now := time.Now()
	if _, e := f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=$2 WHERE id=$1", f.owner, now); e != nil {
		t.Fatal(e)
	}
	a, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := f.st.Queries.ListWorkspaces(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	ws := rows[0].ID
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO hosted_subscriptions(workspace_id,server_limit) VALUES($1,2)", ws); e != nil {
		t.Fatal(e)
	}
	return f, store.Principal{Account: a, MFA: true, Recent: true}, ws
}
func TestHostedSignupRequiresOneUseWorkspaceAdmission(t *testing.T) {
	f := setupBootstrap(t, false)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 1, ServerLimit: 2}
	ctx := context.Background()
	if e := f.auth.InviteHosted(ctx, "beta@example.test", f.apiServer.Origin); e != nil {
		t.Fatal(e)
	}
	token := tokenFromLinkMail(t, f, "/signup")
	browser := f.browser(t)
	var session api.Session
	browser.call("POST", "/api/v1/hosting/signup", map[string]string{"token": token, "workspace": "Beta", "name": "Owner", "password": f.password}, 200, &session)
	if session.User == nil || !session.User.EmailVerified || len(session.Workspaces) != 1 || session.CreatedWorkspaceId == nil || *session.CreatedWorkspaceId != session.Workspaces[0].Id {
		t.Fatalf("signup did not establish verified owner: %+v", session)
	}
	browser.csrf = session.CsrfToken
	browser.call("POST", "/api/v1/workspaces", map[string]string{"name": "Bypass"}, 400, nil)
	browser.call("POST", "/api/v1/hosting/signup", map[string]string{"token": token, "workspace": "Reuse"}, 400, nil)
	if e := f.auth.InviteHosted(ctx, "second@example.test", f.apiServer.Origin); e == nil {
		t.Fatal("cohort allowance bypassed")
	}
}

type stripeFixture struct {
	t            *testing.T
	mu           sync.Mutex
	customer     string
	sessions     map[string]hosting.Checkout
	keys         map[string]bool
	until        time.Time
	status       string
	loseResponse bool
	workspace    string
}

func (s *stripeFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Host != "api.stripe.com" || r.Header.Get("Stripe-Version") != hosting.StripeVersion {
		s.t.Error("unexpected billing boundary")
	}
	_ = r.ParseForm()
	var body any
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/customers":
		body = map[string]any{"id": s.customer}
	case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
		if s.workspace != "" {
			for _, key := range []string{"success_url", "cancel_url"} {
				u, e := url.Parse(r.Form.Get(key))
				marker := "returned"
				if key == "cancel_url" {
					marker = "cancelled"
				}
				if e != nil || u.Query().Get("workspace") != s.workspace || u.Path != "/onboarding" || u.Query().Get("billing") != marker {
					s.t.Error("checkout return lost its workspace")
				}
			}
		}
		key := r.Header.Get("Idempotency-Key")
		s.keys[key] = true
		v, ok := s.sessions[key]
		if !ok {
			v = hosting.Checkout{ID: "cs_fixture", URL: "https://checkout.stripe.com/c/pay/fixture", Customer: s.customer, Reference: r.Form.Get("client_reference_id"), Status: "open"}
			fmt.Sscan(r.Form.Get("expires_at"), &v.Expires)
			s.sessions[key] = v
		}
		if s.loseResponse {
			s.loseResponse = false
			return nil, fmt.Errorf("response lost")
		}
		body = v
	case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
		u, e := url.Parse(r.Form.Get("return_url"))
		if e != nil || u.Query().Get("workspace") != s.workspace {
			s.t.Error("portal return lost its workspace")
		}
		body = map[string]any{"url": "https://billing.stripe.com/p/session/fixture"}
	case r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions":
		list := []hosting.Checkout{}
		for _, v := range s.sessions {
			list = append(list, v)
		}
		body = map[string]any{"data": list, "has_more": false}
	case strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
		for _, v := range s.sessions {
			body = v
		}
	case r.URL.Path == "/v1/subscriptions/sub_fixture":
		body = map[string]any{"id": "sub_fixture", "customer": s.customer, "status": s.status, "items": map[string]any{"data": []any{map[string]any{"quantity": 1, "current_period_end": s.until.Unix(), "price": map[string]any{"id": "price_monthly", "recurring": map[string]any{"interval": "month", "interval_count": 1}}}}}}
	default:
		s.t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
		return nil, fmt.Errorf("unexpected call")
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
}
func (s *stripeFixture) complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.sessions {
		v.Status = "complete"
		v.Subscription = "sub_fixture"
		s.sessions[k] = v
	}
}
func signedEvent(t *testing.T, kind, id string, object any) ([]byte, string) {
	t.Helper()
	raw, e := json.Marshal(map[string]any{"id": id, "type": kind, "data": map[string]any{"object": object}})
	if e != nil {
		t.Fatal(e)
	}
	stamp := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte("whsec_fixture"))
	fmt.Fprintf(mac, "%s.%s", stamp, raw)
	return raw, "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
func TestHostedBillingSerializesCheckoutAndFencesExpiredWork(t *testing.T) {
	f, p, ws := hostedFixture(t)
	ctx := context.Background()
	stripe := &stripeFixture{t: t, customer: "cus_fixture", sessions: map[string]hosting.Checkout{}, keys: map[string]bool{}, until: time.Now().Add(time.Hour), status: "active"}
	stripe.workspace = ws.String()
	svc := &billing.Service{Store: f.st, Stripe: hosting.Stripe{Key: "fixture", Price: "price_monthly", HTTP: &http.Client{Transport: stripe}}, Origin: "https://panel.example.test", Now: time.Now}
	f.apiServer.Billing = svc
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			url, e := svc.Checkout(ctx, p, ws)
			if e != nil || !strings.HasPrefix(url, "https://checkout.stripe.com/") {
				t.Errorf("checkout %s %v", url, e)
			}
		})
	}
	wg.Wait()
	if len(stripe.keys) != 1 {
		t.Fatalf("created %d distinct checkout intents", len(stripe.keys))
	}
	stripe.complete()
	var session hosting.Checkout
	for _, v := range stripe.sessions {
		session = v
	}
	raw, sig := signedEvent(t, "checkout.session.completed", "evt_first", session)
	if e := svc.Webhook(ctx, raw, sig); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Portal(ctx, p, ws); e != nil {
		t.Fatal(e)
	}
	if e := svc.Webhook(ctx, raw, sig); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Checkout(ctx, p, ws); e == nil {
		t.Fatal("second subscription admitted")
	}
	status, e := svc.Status(ctx, p, ws)
	if e != nil || status.ManagementUntil == nil {
		t.Fatalf("missing entitlement: %+v %v", status, e)
	}
	one, e := f.execution.CreateServer(ctx, p, ws, api.CreateServer{Name: "One", Role: "deployment"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.CreateServer(ctx, p, ws, api.CreateServer{Name: "Two", Role: "build"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.CreateServer(ctx, p, ws, api.CreateServer{Name: "Excess", Role: "build"}); e == nil {
		t.Fatal("server allowance bypassed")
	}
	stripe.mu.Lock()
	stripe.until = time.Now().Add(-time.Minute)
	stripe.mu.Unlock()
	raw, sig = signedEvent(t, "customer.subscription.updated", "evt_expire", map[string]any{"id": "sub_fixture", "customer": stripe.customer})
	if e = svc.Webhook(ctx, raw, sig); e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.CreateServer(ctx, p, ws, api.CreateServer{Name: "Expired", Role: "deployment"}); e != store.SubscriptionRequired {
		t.Fatalf("expiry not enforced: %v", e)
	}
	if e = f.execution.Control(ctx, p, ws, one.Server.Id, true); e != nil {
		t.Fatal("expired owner could not revoke the source connection for transfer", e)
	}
	if e = svc.Webhook(ctx, raw, strings.Replace(sig, "v1=", "v1=00", 1)); e == nil {
		t.Fatal("invalid signature accepted")
	}
	// An old event retrieves the provider's current state and cannot restore access.
	raw, sig = signedEvent(t, "checkout.session.completed", "evt_delayed", session)
	if e = svc.Webhook(ctx, raw, sig); e != nil {
		t.Fatal(e)
	}
	status, e = svc.Status(ctx, p, ws)
	if e != nil || status.ManagementUntil.After(time.Now()) {
		t.Fatalf("old event restored access: %+v %v", status, e)
	}
	other := uuid.New()
	if _, e = svc.Status(ctx, p, other); e == nil {
		t.Fatal("cross-workspace status accepted")
	}
}
func TestHostedCheckoutReconcilesLostCompletedResponse(t *testing.T) {
	f, p, ws := hostedFixture(t)
	ctx := context.Background()
	stripe := &stripeFixture{t: t, customer: "cus_fixture", sessions: map[string]hosting.Checkout{}, keys: map[string]bool{}, until: time.Now().Add(time.Hour), status: "active", loseResponse: true}
	svc := &billing.Service{Store: f.st, Stripe: hosting.Stripe{Key: "fixture", Price: "price_monthly", HTTP: &http.Client{Transport: stripe}}, Origin: "https://panel.example.test", Now: time.Now}
	if _, e := svc.Checkout(ctx, p, ws); e == nil {
		t.Fatal("expected lost response")
	}
	stripe.complete()
	if _, e := f.st.Pool.Exec(ctx, "UPDATE hosted_checkouts SET expires_at=now()-interval '1 second' WHERE workspace_id=$1", ws); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Checkout(ctx, p, ws); e == nil {
		t.Fatal("completed checkout should direct owner to billing portal")
	}
	status, e := svc.Status(ctx, p, ws)
	if e != nil || status.ManagementUntil == nil {
		t.Fatalf("completion not reconciled: %+v %v", status, e)
	}
	if len(stripe.keys) != 1 {
		t.Fatal("replacement checkout created after lost completion")
	}
}
