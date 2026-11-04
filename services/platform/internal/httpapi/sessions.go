package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/store"
)

var errSessionRevoked = errors.New("browser session was revoked")
var errSessionContext = errors.New("browser session context is unavailable")
var errSessionResponse = errors.New("browser session response is unavailable")

type sessionLoadKey struct{}
type sessionLoad struct {
	token string
	// moveTerminals defers deleting the old token so the renewed session, the old
	// session's removal and its terminal bindings commit together.
	moveTerminals bool
}

type sessionStore struct {
	*pgxstore.PostgresStore
	queries *db.Queries
	store   *store.Store
}

func (s *sessionStore) DeleteCtx(ctx context.Context, token string) error {
	if loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad); loaded != nil && loaded.moveTerminals && loaded.token == token {
		return nil
	}
	return s.PostgresStore.DeleteCtx(ctx, token)
}

// renewWithTerminals replaces a token in one transaction, so a re-check of an open
// terminal session finds exactly one of them.
func (s *sessionStore) renewWithTerminals(ctx context.Context, previous, token string, data []byte, expiry time.Time) error {
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
		// A concurrent logout/renewal or expiry cleanup may have deleted the row.
		// UPDATE serializes with that deletion and never recreates its authority.
		n, err := s.queries.RefreshSession(ctx, db.RefreshSessionParams{Token: token, Data: data, Expiry: expiry})
		if err != nil {
			return err
		}
		if n == 0 {
			return errSessionRevoked
		}
	} else if loaded.moveTerminals && loaded.token != "" {
		if err := s.renewWithTerminals(ctx, loaded.token, token, data, expiry); err != nil {
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

type sessionResponseGuard struct {
	http.ResponseWriter
	failed bool
}

func (w *sessionResponseGuard) WriteHeader(status int) {
	if !w.failed {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *sessionResponseGuard) Write(data []byte) (int, error) {
	if w.failed {
		return 0, errSessionResponse
	}
	return w.ResponseWriter.Write(data)
}
func (w *sessionResponseGuard) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *sessionResponseGuard) FlushError() error {
	if w.failed {
		return errSessionResponse
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func browserSessionRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), sessionLoadKey{}, &sessionLoad{})
		next.ServeHTTP(&sessionResponseGuard{ResponseWriter: w}, r.WithContext(ctx))
	})
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
		// recovery code changes the account's version, which ends them, and another request
		// may then have removed the old session, so there is nothing to move.
		r.Context().Value(sessionLoadKey{}).(*sessionLoad).moveTerminals = a.AuthVersion == principal(r.Context()).Account.AuthVersion
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
