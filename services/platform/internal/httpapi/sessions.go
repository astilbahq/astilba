package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
)

var errSessionRevoked = errors.New("browser session was revoked")
var errSessionContext = errors.New("browser session context is unavailable")
var errSessionResponse = errors.New("browser session response is unavailable")

type sessionLoadKey struct{}
type sessionLoad struct {
	token string
	// replaceLive defers deleting the old token so the renewed session and the old
	// session's removal commit together, and the renewal fails if the old row has gone.
	replaceLive bool
	// moveTerminals also carries the old session's terminal bindings to the new token.
	moveTerminals bool
	// presented is the cookie the browser sent, whether or not its session is alive.
	presented string
	// keepCookie leaves a destroyed session's cookie in the browser. See dropStaleSession.
	keepCookie bool
	// keepRow leaves a destroyed session's row in place. See dropStaleSession.
	keepRow bool
	// device is sent unless saving the session failed. See issueDevice.
	device *http.Cookie
}

type sessionStore struct {
	*pgxstore.PostgresStore
	queries *db.Queries
	store   *store.Store
}

func (s *sessionStore) DeleteCtx(ctx context.Context, token string) error {
	if loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad); loaded != nil && (loaded.replaceLive || loaded.keepRow) && loaded.token == token {
		return nil
	}
	return s.PostgresStore.DeleteCtx(ctx, token)
}

// renewLive replaces a token in one transaction, so a re-check of an open terminal
// session finds exactly one of them.
func (s *sessionStore) renewLive(ctx context.Context, previous, token string, data []byte, expiry time.Time, moveTerminals bool) error {
	oldHash, err := base64.RawURLEncoding.DecodeString(previous)
	if err != nil {
		return err
	}
	newHash, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return err
	}
	return s.store.Transaction(ctx, func(q *db.Queries) error {
		// A logout or another renewal may have won: never recreate its authority.
		if n, err := q.DeleteRenewedSession(ctx, previous); err != nil {
			return err
		} else if n == 0 {
			return errSessionRevoked
		}
		if err := q.InsertSession(ctx, db.InsertSessionParams{Token: token, Data: data, Expiry: expiry}); err != nil {
			return err
		}
		if !moveTerminals {
			return nil
		}
		return q.MoveTroubleshootingBrowserBinding(ctx, db.MoveTroubleshootingBrowserBindingParams{Renewed: newHash, Previous: oldHash})
	})
}

func (s *sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return nil, false, errSessionContext
	}
	data, found, err := s.PostgresStore.FindCtx(ctx, token)
	if err == nil && found {
		loaded.token = token
	}
	return data, found, err
}

// SCS must use its context-aware store API to distinguish a loaded token from
// a freshly minted token. A context-free save cannot establish that distinction.
func (s *sessionStore) Commit(string, []byte, time.Time) error { return errSessionContext }

func (s *sessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return errSessionContext
	}
	if loaded.token == token {
		// A concurrent logout/renewal or expiry cleanup may have deleted the row, or the
		// session may have expired during this request. UPDATE serializes with that
		// deletion and never restores its authority.
		n, err := s.queries.RefreshSession(ctx, db.RefreshSessionParams{Token: token, Data: data, Expiry: expiry})
		if err != nil {
			return err
		}
		if n == 0 {
			return errSessionRevoked
		}
	} else if loaded.replaceLive {
		if err := s.renewLive(ctx, loaded.token, token, data, expiry, loaded.moveTerminals); err != nil {
			return err
		}
	} else {
		if err := s.queries.InsertSession(ctx, db.InsertSessionParams{Token: token, Data: data, Expiry: expiry}); err != nil {
			return err
		}
	}
	loaded.token = token
	return nil
}

// sessionResponseGuard saves the session before the first response byte, as SCS's
// LoadAndSave does.
type sessionResponseGuard struct {
	http.ResponseWriter
	server  *Server
	request *http.Request
	loaded  *sessionLoad
	saved   bool
	failed  bool
}

// save replaces a cookie the browser sent only when this request replaced or deliberately
// ended its session. A browser applies Set-Cookie in arrival order, so anything else sent
// to a request carrying an old token could undo the cookie a credential change has just
// renewed. Idle refreshes need no cookie: it lasts to the session's absolute deadline and
// the row's expiry enforces the idle timeout.
func (w *sessionResponseGuard) save() {
	if w.saved {
		return
	}
	w.saved = true
	sessions, ctx := w.server.Sessions, w.request.Context()
	switch sessions.Status(ctx) {
	case scs.Modified:
		token, _, err := sessions.Commit(ctx)
		if err != nil {
			w.fail(w.request, err)
		} else if token != w.loaded.presented {
			sessions.WriteSessionCookie(ctx, w, token, sessions.Deadline(ctx))
		}
	case scs.Destroyed:
		if !w.loaded.keepCookie {
			sessions.WriteSessionCookie(ctx, w, "", time.Time{})
		}
	}
	if w.loaded.device != nil && !w.failed {
		http.SetCookie(w, w.loaded.device)
	}
}

// fail answers a failed load or save. The handler keeps writing after a failed save, so
// its payload and download headers must stay out of this response.
func (w *sessionResponseGuard) fail(r *http.Request, err error) {
	w.failed = true
	w.Header().Del("Content-Disposition")
	if errors.Is(err, errSessionRevoked) {
		err = domain.Unauthenticated
	}
	w.server.fail(w.ResponseWriter, r, err)
}

func (w *sessionResponseGuard) WriteHeader(status int) {
	w.save()
	if !w.failed {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *sessionResponseGuard) Write(data []byte) (int, error) {
	w.save()
	if w.failed {
		return 0, errSessionResponse
	}
	return w.ResponseWriter.Write(data)
}
func (w *sessionResponseGuard) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *sessionResponseGuard) FlushError() error {
	w.save()
	if w.failed {
		return errSessionResponse
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (s *Server) browserSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Cookie")
		loaded := &sessionLoad{}
		if cookie, err := r.Cookie(s.Sessions.Cookie.Name); err == nil {
			loaded.presented = cookie.Value
		}
		guard := &sessionResponseGuard{ResponseWriter: w, server: s, loaded: loaded}
		ctx, err := s.Sessions.Load(context.WithValue(r.Context(), sessionLoadKey{}, loaded), loaded.presented)
		if err != nil {
			guard.fail(r, err)
			return
		}
		guard.request = r.WithContext(ctx)
		next.ServeHTTP(guard, guard.request)
		guard.save()
	})
}

// rotate replaces the token after an account change on an authenticated session.
// None of these changes proves a fresh credential: a recent session is enough, and
// ConfirmMFA's code comes from a secret that session was just given. They therefore
// keep the session's absolute deadline and recent authentication, and a session that
// a logout has deleted stays deleted.
func (s *Server) rotate(ctx context.Context, a db.Account, mfa bool) error {
	ctx.Value(sessionLoadKey{}).(*sessionLoad).replaceLive = true
	deadline := s.Sessions.Deadline(ctx)
	recent := s.Sessions.GetTime(ctx, "recent")
	if err := s.establish(ctx, a, mfa); err != nil {
		return err
	}
	s.Sessions.SetDeadline(ctx, deadline)
	s.Sessions.Put(ctx, "recent", recent)
	return nil
}

// dropStaleSession ends a session whose account has moved on, without touching the
// browser's cookie: this request may have been overtaken by the credential change that
// renewed it, and a dead token grants nothing. keepRow also leaves the row for an account
// that still exists, because that change may still be replacing it and must not see it
// removed as if by a logout, and a token on an older account version grants nothing.
func (s *Server) dropStaleSession(ctx context.Context, keepRow bool) error {
	loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	loaded.keepCookie, loaded.keepRow = true, keepRow
	return s.Sessions.Destroy(ctx)
}

// csrfToken returns the token a mutation must present. With no live session it derives
// from the cookie the browser already holds, so a request carrying a dead token can be
// answered without replacing a cookie that may have been renewed meanwhile. It authorizes
// only what an anonymous session may do. Like a stored token it reaches only same-origin
// script, and knowing it requires the unguessable cookie, which __Host- stops another
// host planting; the hash keeps that cookie from script.
func (s *Server) csrfToken(ctx context.Context) string {
	token := s.Sessions.GetString(ctx, "csrf")
	loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if token != "" || s.Sessions.Token(ctx) != "" || loaded.presented == "" {
		return token
	}
	derived := sha256.Sum256([]byte("astilba dead session csrf\x00" + loaded.presented))
	return base64.RawURLEncoding.EncodeToString(derived[:])
}

func (s *Server) Reauthenticate(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Reauthenticate](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	a, e := s.Auth.Reauthenticate(r.Context(), principal(r.Context()).Account.ID, value(b.Password), value(b.Code))
	if e == nil {
		// Confirming credentials keeps open terminal sessions, unlike any other renewal. A
		// recovery code changes the account's version, which ends them, so there is nothing
		// to move.
		loaded := r.Context().Value(sessionLoadKey{}).(*sessionLoad)
		loaded.replaceLive = true
		loaded.moveTerminals = a.AuthVersion == principal(r.Context()).Account.AuthVersion
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
