package httpapi

import (
	"io"
	"net/http"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/google/uuid"
)

func (s *Server) billing() *billing.Service {
	if s.Billing != nil {
		return s.Billing
	}
	return &billing.Service{Store: s.Store, Stripe: hosting.Stripe{Key: s.Store.Hosting.SecretKey, Price: s.Store.Hosting.PriceID}, Origin: s.Origin, Now: s.Auth.Now}
}
func (s *Server) HostingInfo(w http.ResponseWriter, r *http.Request) {
	s.respond(w, 200, api.HostedInfo{Enabled: s.Store.Hosted, BillingEnabled: s.Store.Hosting.BillingEnabled(), Support: s.Store.Hosting.Support})
}
func (s *Server) HostedSignup(w http.ResponseWriter, r *http.Request) {
	if e := s.Auth.Limit(r.Context(), "hosted-signup:ip:"+s.clientIP(r), 10, 15*time.Minute); e != nil {
		s.fail(w, r, e)
		return
	}
	b, e := decode[api.HostedSignup](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	a, _, e := s.Auth.AcceptHosted(r.Context(), value(b.Token), value(b.Name), value(b.Password), b.Workspace, principal(r.Context()))
	if e == nil && principal(r.Context()) == nil {
		e = s.establish(r.Context(), a, false)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) RequestEmailVerification(w http.ResponseWriter, r *http.Request) {
	e := s.Auth.RequestEmailVerification(r.Context(), *principal(r.Context()))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.VerificationCode](r)
	if e == nil {
		e = s.Auth.VerifyEmail(r.Context(), *principal(r.Context()), value(b.Token))
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) Subscription(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.billing().Status(r.Context(), *principal(r.Context()), ws)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, v)
}
func (s *Server) Checkout(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	url, e := s.billing().Checkout(r.Context(), *principal(r.Context()), ws)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.Redirect{Url: url})
}
func (s *Server) BillingPortal(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	url, e := s.billing().Portal(r.Context(), *principal(r.Context()), ws)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.Redirect{Url: url})
}
func (s *Server) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	raw, e := io.ReadAll(io.LimitReader(r.Body, 262145))
	if e != nil || len(raw) > 262144 {
		s.fail(w, r, domain.Invalid("body"))
		return
	}
	if e = s.billing().Webhook(r.Context(), raw, r.Header.Get("Stripe-Signature")); e != nil {
		s.fail(w, r, e)
		return
	}
	w.WriteHeader(204)
}
