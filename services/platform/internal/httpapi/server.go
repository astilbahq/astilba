package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/gob"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/alerts"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/billing"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	middleware "github.com/oapi-codegen/nethttp-middleware"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func init() { gob.Register(time.Time{}) }

type contextKey int

const (
	principalKey contextKey = iota
	requestIDKey
)

type Server struct {
	Alerts            *alerts.Service
	Store             *store.Store
	Billing           *billing.Service
	Auth              *auth.Service
	Sessions          *scs.SessionManager
	Origin            string
	Log               *slog.Logger
	Execution         *execution.Service
	ArtifactDirectory string
	GitHubConfig      githubapp.Config
	Development       bool
	TrustedProxies    []netip.Prefix
}

func New(st *store.Store, service *auth.Service, origin string, development bool, log *slog.Logger) (*Server, func()) {
	sessions := scs.New()
	backend := pgxstore.New(st.Pool)
	sessions.Store = backend
	sessions.HashTokenInStore = true
	sessions.Lifetime = 12 * time.Hour
	sessions.IdleTimeout = 30 * time.Minute
	sessions.Cookie.Name = "__Host-astilba_session"
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteStrictMode
	sessions.Cookie.Secure = true
	sessions.Cookie.Path = "/"
	if development {
		sessions.Cookie.Name = "astilba_session"
		sessions.Cookie.Secure = false
	}
	return &Server{Alerts: alerts.New(st, service.Mail, origin, development, strings.Split(os.Getenv("ASTILBA_ALERT_WEBHOOK_ORIGINS"), ",")), Store: st, Auth: service, Sessions: sessions, Origin: origin, Log: log, Development: development}, backend.StopCleanup
}
func (s *Server) Handler(assets http.Handler) (http.Handler, error) {
	if s.Execution == nil {
		var e error
		s.Execution, e = execution.New(s.Store)
		if e != nil {
			return nil, e
		}
	}
	s.Execution.Policy.Development = s.Development
	if s.Execution.GitHub == nil {
		var e error
		s.Execution.GitHub, e = githubapp.New(s.GitHubConfig, s.Origin, s.Development)
		if e != nil {
			return nil, e
		}
	}
	if s.Execution.GitHub != nil {
		s.Execution.Policy.GitHubFixture = s.Execution.GitHub.FixtureAPI()
	}
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Use(s.request)
	r.Group(func(r chi.Router) {
		r.Use(s.protection)
		r.Use(s.archiveAdmission)
		r.Use(middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
			Options: openapi3filter.Options{AuthenticationFunc: s.authenticateAPI},
			ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, _ middleware.ErrorHandlerOpts) {
				if p := principal(ctx); p != nil && p.Automation != nil {
					var security *openapi3filter.SecurityRequirementsError
					if errors.As(err, &security) {
						s.fail(w, r, domain.Forbidden)
						return
					}
				}
				if principal(ctx) == nil && agentIdentity(ctx).ID == uuid.Nil && !publicPath(r.URL.Path) && r.URL.Path != "/agent/v1/enroll" {
					s.fail(w, r, domain.Unauthenticated)
					return
				}
				s.fail(w, r, domain.Invalid("request"))
			},
		}))
		api.HandlerFromMuxWithBaseURL(s, r, "")
	})
	r.Get("/agent-artifacts/{sha}", s.agentArtifact)
	r.Get("/agent-artifacts/{sha}/manifest.json", s.agentManifest)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.Store.Pool.Ping(ctx) != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	r.Handle("/*", assets)
	return r, nil
}
func publicPath(p string) bool {
	if strings.HasPrefix(p, "/hooks/git/") || p == "/hooks/stripe" || p == "/hooks/github-app" {
		return true
	}
	switch p {
	case "/api/v1/hosting", "/api/v1/hosting/signup", "/api/v1/session", "/api/v1/auth/login", "/api/v1/auth/mfa", "/api/v1/auth/reset/request", "/api/v1/auth/reset/complete", "/api/v1/auth/invitations/accept":
		return true
	}
	return false
}
func principal(ctx context.Context) *store.Principal {
	p, _ := ctx.Value(principalKey).(*store.Principal)
	return p
}
func (s *Server) request(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		defer func() {
			if recover() != nil {
				s.Log.Error("request panic", "request_id", id)
				s.fail(w, r, errors.New("panic"))
			}
		}()
		recorder := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(recorder, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		s.Log.Info("request", "request_id", id, "route", route, "status", recorder.Status())
	})
}
func (s *Server) identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, err := uuid.Parse(s.Sessions.GetString(r.Context(), "account"))
		if err == nil {
			a, e := s.Store.Queries.AccountByID(r.Context(), id)
			if e != nil || a.AuthVersion != s.Sessions.GetInt64(r.Context(), "version") {
				if e = s.Sessions.Destroy(r.Context()); e != nil {
					s.fail(w, r, e)
					return
				}
			} else {
				p := &store.Principal{Account: a, MFA: s.Sessions.GetBool(r.Context(), "mfa"), Recent: store.Recent(s.Sessions.GetTime(r.Context(), "recent"), s.Auth.Now())}
				r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			token := s.Sessions.GetString(r.Context(), "csrf")
			if r.Header.Get("Origin") != s.Origin || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
				s.fail(w, r, domain.Forbidden)
				return
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				s.fail(w, r, domain.Invalid("content-type"))
				return
			}
		}
		limit := int64(150000)
		if r.URL.Path == "/api/v1/workspaces/import" {
			limit = 45_000_000
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
func (s *Server) respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	err = store.Normalize(err)
	status := 500
	out := api.Error{Code: "internal_error", Message: "The request could not be completed.", RequestId: r.Context().Value(requestIDKey).(string)}
	var de *domain.Error
	if errors.As(err, &de) {
		out.Code = de.Code
		out.Message = de.Message
		if len(de.Fields) > 0 {
			out.Fields = &de.Fields
		}
		switch de.Code {
		case "unauthenticated":
			status = 401
		case "forbidden", "mfa_required", "recent_auth_required", "invitation_signin_required":
			status = 403
		case "not_found":
			status = 404
		case "conflict":
			status = 409
		case "rate_limited":
			status = 429
		default:
			status = 400
		}
	} else {
		s.Log.Error("request failed", "request_id", out.RequestId)
	}
	if status == 429 {
		w.Header().Set("Retry-After", "300")
	}
	s.respond(w, status, out)
}
func decode[T any](r *http.Request) (T, error) {
	var v T
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return v, domain.Invalid("request")
	}
	return v, nil
}
func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (s *Server) establish(ctx context.Context, a db.Account, mfa bool) error {
	s.Sessions.Remove(ctx, "github-session")
	if err := s.Sessions.RenewToken(ctx); err != nil {
		return err
	}
	s.Sessions.Remove(ctx, "pending")
	s.Sessions.Remove(ctx, "pending-version")
	s.Sessions.Remove(ctx, "pending-at")
	s.Sessions.Put(ctx, "account", a.ID.String())
	s.Sessions.Put(ctx, "version", a.AuthVersion)
	s.Sessions.Put(ctx, "mfa", mfa)
	s.Sessions.Put(ctx, "recent", s.Auth.Now())
	token, err := cryptobox.Token()
	if err == nil {
		s.Sessions.Put(ctx, "csrf", token)
	}
	return err
}
func (s *Server) session(ctx context.Context) (api.Session, error) {
	token := s.Sessions.GetString(ctx, "csrf")
	if token == "" {
		var err error
		token, err = cryptobox.Token()
		if err != nil {
			return api.Session{}, err
		}
		s.Sessions.Put(ctx, "csrf", token)
	}
	out := api.Session{CsrfToken: token, Workspaces: []api.Workspace{}, MfaPending: s.Sessions.GetString(ctx, "pending") != "" && store.Recent(s.Sessions.GetTime(ctx, "pending-at"), s.Auth.Now())}
	id, err := uuid.Parse(s.Sessions.GetString(ctx, "account"))
	if err != nil {
		return out, nil
	}
	a, err := s.Store.Queries.AccountByID(ctx, id)
	if err != nil {
		return out, err
	}
	count, err := s.Store.Queries.CountRecoveryCodes(ctx, id)
	if err != nil {
		return out, err
	}
	out.User = &api.Account{Id: a.ID, Name: a.Name, Email: openapi_types.Email(a.Email), MfaEnabled: len(a.TotpSecret) > 0, RecoveryCodesRemaining: int(count), EmailVerified: a.EmailVerifiedAt != nil}
	out.RecentAuth = store.Recent(s.Sessions.GetTime(ctx, "recent"), s.Auth.Now())
	ws, err := s.Store.Queries.ListWorkspaces(ctx, id)
	for _, w := range ws {
		out.Workspaces = append(out.Workspaces, api.Workspace{Id: w.ID, Name: w.Name, Role: api.Role(w.Role), RequireMfa: w.RequireMfa})
	}
	return out, err
}
func (s *Server) sendSession(w http.ResponseWriter, r *http.Request) {
	v, e := s.session(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, v)
}
func (s *Server) Session(w http.ResponseWriter, r *http.Request) { s.sendSession(w, r) }
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Login](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.Sessions.Remove(r.Context(), "github-session")
	a, e := s.Auth.Login(r.Context(), string(b.Email), value(b.Password), s.clientIP(r))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if len(a.TotpSecret) > 0 {
		if e = s.Sessions.RenewToken(r.Context()); e == nil {
			s.Sessions.Remove(r.Context(), "account")
			s.Sessions.Put(r.Context(), "pending", a.ID.String())
			s.Sessions.Put(r.Context(), "pending-version", a.AuthVersion)
			s.Sessions.Put(r.Context(), "pending-at", s.Auth.Now())
		}
	} else {
		e = s.establish(r.Context(), a, false)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Code](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	id, e := uuid.Parse(s.Sessions.GetString(r.Context(), "pending"))
	if e != nil || !store.Recent(s.Sessions.GetTime(r.Context(), "pending-at"), s.Auth.Now()) {
		s.fail(w, r, domain.Credentials)
		return
	}
	a, e := s.Auth.VerifyCode(r.Context(), id, s.Sessions.GetInt64(r.Context(), "pending-version"), b.Code)
	if e == nil {
		e = s.establish(r.Context(), a, true)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if e := s.Sessions.Destroy(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) Reauthenticate(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Reauthenticate](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	a, e := s.Auth.Reauthenticate(r.Context(), principal(r.Context()).Account.ID, value(b.Password), value(b.Code))
	if e == nil {
		nonce := s.Sessions.GetString(r.Context(), "github-session")
		e = s.establish(r.Context(), a, len(a.TotpSecret) > 0)
		if e == nil && nonce != "" {
			s.Sessions.Put(r.Context(), "github-session", nonce)
		}
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) recent(r *http.Request) error {
	if p := principal(r.Context()); p == nil {
		return domain.Unauthenticated
	} else if !p.Recent {
		return domain.RecentRequired
	}
	return nil
}
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.NewPassword](r)
	if e == nil {
		e = s.recent(r)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	a, e := s.Auth.ChangePassword(r.Context(), principal(r.Context()).Account.ID, principal(r.Context()).Account.AuthVersion, value(b.Password))
	if e == nil {
		e = s.establish(r.Context(), a, len(a.TotpSecret) > 0)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.ResetRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	e = s.Auth.RequestReset(r.Context(), string(b.Email), s.clientIP(r), s.Origin)
	if errors.Is(e, domain.RateLimited) {
		s.fail(w, r, e)
		return
	}
	if e != nil {
		s.Log.Warn("password reset delivery unavailable")
	}
	w.WriteHeader(202)
}
func (s *Server) CompletePasswordReset(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.ResetComplete](r)
	if e == nil {
		e = s.Auth.CompleteReset(r.Context(), b.Token, value(b.Password), s.clientIP(r))
	}
	if e == nil {
		e = s.Sessions.Destroy(r.Context())
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.AcceptInvitation](r)
	if e == nil {
		e = s.Auth.Limit(r.Context(), "invite:"+s.clientIP(r), 20, 15*time.Minute)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	var current *db.Account
	if p := principal(r.Context()); p != nil {
		current = &p.Account
	}
	a, e := s.Auth.AcceptInvitation(r.Context(), b.Token, value(b.Name), value(b.Password), current)
	if e == nil && (current == nil || current.ID != a.ID) {
		e = s.establish(r.Context(), a, false)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
func (s *Server) EnrollMFA(w http.ResponseWriter, r *http.Request) {
	if e := s.recent(r); e != nil {
		s.fail(w, r, e)
		return
	}
	secret, uri, e := s.Auth.BeginMFA(r.Context(), principal(r.Context()).Account)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.MFAEnrollment{Secret: secret, Uri: uri})
}
func (s *Server) ConfirmMFA(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Code](r)
	if e == nil {
		e = s.recent(r)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	a, codes, e := s.Auth.ConfirmMFA(r.Context(), principal(r.Context()).Account.ID, principal(r.Context()).Account.AuthVersion, b.Code)
	s.sendCodes(w, r, a, codes, e)
}
func (s *Server) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if e := s.recent(r); e != nil {
		s.fail(w, r, e)
		return
	}
	a, codes, e := s.Auth.RegenerateRecovery(r.Context(), principal(r.Context()).Account.ID, principal(r.Context()).Account.AuthVersion)
	s.sendCodes(w, r, a, codes, e)
}
func (s *Server) sendCodes(w http.ResponseWriter, r *http.Request, a db.Account, codes []string, e error) {
	if e == nil {
		e = s.establish(r.Context(), a, true)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	session, e := s.session(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.RecoveryCodes{Codes: codes, Session: session})
}
func (s *Server) DisableMFA(w http.ResponseWriter, r *http.Request) {
	if e := s.recent(r); e != nil {
		s.fail(w, r, e)
		return
	}
	a, e := s.Auth.DisableMFA(r.Context(), principal(r.Context()).Account.ID, principal(r.Context()).Account.AuthVersion)
	if e == nil {
		e = s.establish(r.Context(), a, false)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
