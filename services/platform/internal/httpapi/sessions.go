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
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errSessionRevoked = errors.New("browser session was revoked")
var errSessionContext = errors.New("browser session context is unavailable")
var errRenewalOutsideTransaction = errors.New("a renewed browser session is stored only by storeSession")
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
	// deviceToken is sent as the device cookie unless saving the session failed or its renewal
	// was discarded. See issueDevice and renewToken.
	deviceToken string
	// lineage is the loaded session's chain of renewed tokens, which a logout ends.
	lineage uuid.UUID
	// staleCSRF is the CSRF token of a loaded session that dropStaleSession ended. See csrfToken.
	staleCSRF string
	// continueLineage gives a new sign-in's token the loaded session's lineage while that
	// session lasts, deleting it with the new token's insert. See VerifyMFA and continueSignIn.
	continueLineage bool
	// renewed is set once this request has replaced the session's token, which then commits
	// only with a successful response. See renewToken.
	renewed bool
	// tx is the transaction of the sign-in or credential change the session is stored with, while
	// storeSession runs, and stored is set once the session is stored there. See storeSession.
	tx     pgx.Tx
	stored bool
	// client summarizes the browser making this request, which a signed-in session records.
	client sessionClient
	// account and version are what the session being saved is signed in to, if anything. They
	// stay fixed within a token. See save.
	account *uuid.UUID
	version int64
}

// sessionClient is what a signed-in session keeps of the client making its latest request: the
// browser and operating system families, and the network, never the User-Agent or address.
type sessionClient struct{ browser, os, network string }

// row is a new token's row for the session being saved, which signed in at signedIn, or now if
// that is nil. Only a signed-in session records its account, version and client.
func (l *sessionLoad) row(token string, data []byte, expiry time.Time, lineage uuid.UUID, signedIn *time.Time) db.InsertSessionParams {
	return db.InsertSessionParams{
		Token: token, Data: data, Expiry: expiry, Lineage: lineage,
		AccountID: l.account, AuthVersion: pgtype.Int8{Int64: l.version, Valid: l.account != nil}, SignedInAt: signedIn,
		ClientBrowser: l.client.browser, ClientOs: l.client.os, ClientNetwork: pgtype.Text{String: l.client.network, Valid: l.client.network != ""},
	}
}

type sessionStore struct {
	*pgxstore.PostgresStore
	queries *db.Queries
}

// on is where the session is stored: in the transaction of the sign-in or credential change it is
// stored with, if any. See storeSession.
func (s *sessionStore) on(loaded *sessionLoad) *db.Queries {
	if loaded.tx != nil {
		return db.New(loaded.tx)
	}
	return s.queries
}

// renewal is where a renewed token is stored: only in the transaction of the sign-in or
// credential change that renewed it, so the two commit together.
func renewal(loaded *sessionLoad) (*db.Queries, error) {
	if loaded.tx == nil {
		return nil, errRenewalOutsideTransaction
	}
	return db.New(loaded.tx), nil
}

func (s *sessionStore) DeleteCtx(ctx context.Context, token string) error {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return s.PostgresStore.DeleteCtx(ctx, token)
	}
	if (loaded.replaceLive || loaded.continueLineage || loaded.keepRow) && loaded.token == token {
		return nil
	}
	return s.on(loaded).DeleteSession(ctx, token)
}

// endLineage ends a logged-out session together with any token that has renewed it since
// this request loaded it, such as a password change from another tab. See store.EndLineage.
func endLineage(ctx context.Context, st *store.Store, lineage uuid.UUID) error {
	return st.Transaction(ctx, func(q *db.Queries) error { return store.EndLineage(ctx, q, lineage) })
}

// renewLive replaces the loaded token in the renewal's transaction, so a re-check of an open
// terminal session finds exactly one of them. The new token continues the old one's lineage, and keeps
// when it signed in.
func (s *sessionStore) renewLive(ctx context.Context, loaded *sessionLoad, token string, data []byte, expiry time.Time) error {
	previous, lineage := loaded.token, loaded.lineage
	oldHash, err := base64.RawURLEncoding.DecodeString(previous)
	if err != nil {
		return err
	}
	newHash, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return err
	}
	q, err := renewal(loaded)
	if err != nil {
		return err
	}
	if err := q.LockSessionLineage(ctx, lineage); err != nil {
		return err
	}
	// A logout or another renewal may have won: never recreate its authority.
	signedIn, err := q.DeleteRenewedSession(ctx, previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSessionRevoked
	} else if err != nil {
		return err
	}
	if err := q.InsertSession(ctx, loaded.row(token, data, expiry, lineage, signedIn)); err != nil {
		return err
	}
	if !loaded.moveTerminals {
		return nil
	}
	return q.MoveTroubleshootingBrowserBinding(ctx, db.MoveTroubleshootingBrowserBindingParams{Renewed: newHash, Previous: oldHash})
}

// holdTerminals is the auth.Hold of a renewal that moves the loaded token's open terminal
// sessions to the new one: their rows are locked before the transaction locks their account.
func holdTerminals(ctx context.Context, loaded *sessionLoad) auth.Hold {
	previous := loaded.token
	return func(q *db.Queries) error {
		binding, err := base64.RawURLEncoding.DecodeString(previous)
		if err != nil {
			return err
		}
		return q.HoldTroubleshootingBrowserBinding(ctx, binding)
	}
}

// continueSignIn replaces a pending sign-in's token with the signed-in one, continuing its
// lineage, and returns the lineage the new token has. A logout or expiry that ended the pending
// token first ended its lineage, which nothing signs into again: signing in proves a credential,
// so it still succeeds, in a lineage of its own. The lock orders this with that logout.
func (s *sessionStore) continueSignIn(ctx context.Context, loaded *sessionLoad, token string, data []byte, expiry time.Time) (uuid.UUID, error) {
	lineage := loaded.lineage
	q, err := renewal(loaded)
	if err != nil {
		return lineage, err
	}
	if err := q.LockSessionLineage(ctx, lineage); err != nil {
		return lineage, err
	}
	// The pending sign-in had not signed in, so this one does now.
	if _, err := q.DeleteRenewedSession(ctx, loaded.token); errors.Is(err, pgx.ErrNoRows) {
		lineage = uuid.New()
	} else if err != nil {
		return lineage, err
	}
	return lineage, q.InsertSession(ctx, loaded.row(token, data, expiry, lineage, nil))
}

func (s *sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return nil, false, errSessionContext
	}
	row, err := s.queries.FindSession(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	loaded.token, loaded.lineage = token, row.Lineage
	return row.Data, true, nil
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
		client := loaded.client
		n, err := s.queries.RefreshSession(ctx, db.RefreshSessionParams{Token: token, Data: data, Expiry: expiry, ClientBrowser: client.browser, ClientOs: client.os, ClientNetwork: pgtype.Text{String: client.network, Valid: client.network != ""}})
		if err != nil {
			return err
		}
		if n == 0 {
			return errSessionRevoked
		}
	} else if loaded.replaceLive {
		if err := s.renewLive(ctx, loaded, token, data, expiry); err != nil {
			return err
		}
	} else {
		// A new sign-in starts a lineage of its own, even in a browser that had a session,
		// unless it completes the sign-in that session began.
		lineage := uuid.New()
		if loaded.continueLineage {
			var err error
			if lineage, err = s.continueSignIn(ctx, loaded, token, data, expiry); err != nil {
				return err
			}
		} else if err := s.on(loaded).InsertSession(ctx, loaded.row(token, data, expiry, lineage, nil)); err != nil {
			return err
		}
		loaded.lineage = lineage
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
// the row's expiry enforces the idle timeout. status is the response's.
func (w *sessionResponseGuard) save(status int) {
	if w.saved {
		return
	}
	w.saved = true
	sessions, ctx := w.server.Sessions, w.request.Context()
	switch sessions.Status(ctx) {
	case scs.Modified:
		if w.loaded.renewed && status >= 400 {
			// The renewal and what followed it are one unit: a sign-in or credential change
			// that failed after renewing stores no token, so nothing it began is signed in,
			// and it sends neither cookie. See renewToken.
			return
		}
		var token string
		var err error
		if w.loaded.stored {
			// Stored with the sign-in or credential change, whose transaction has committed.
			token = sessions.Token(ctx)
		} else {
			token, err = w.loaded.commit(ctx, sessions)
		}
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
	if w.loaded.deviceToken != "" && !w.failed {
		http.SetCookie(w, w.server.deviceCookie(w.loaded.deviceToken))
	}
}

// commit stores the session, recording what it is signed in to, if anything, and returns its token.
func (l *sessionLoad) commit(ctx context.Context, sessions *scs.SessionManager) (string, error) {
	if account, err := uuid.Parse(sessions.GetString(ctx, "account")); err == nil {
		l.account, l.version = &account, sessions.GetInt64(ctx, "version")
	}
	token, _, err := sessions.Commit(ctx)
	return token, err
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
	w.save(status)
	if !w.failed {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *sessionResponseGuard) Write(data []byte) (int, error) {
	w.save(http.StatusOK)
	if w.failed {
		return 0, errSessionResponse
	}
	return w.ResponseWriter.Write(data)
}
func (w *sessionResponseGuard) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *sessionResponseGuard) FlushError() error {
	w.save(http.StatusOK)
	if w.failed {
		return errSessionResponse
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (s *Server) browserSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Cookie")
		loaded := &sessionLoad{client: s.sessionClient(r)}
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
		guard.save(http.StatusOK)
	})
}

// renewToken replaces the session's token, for a sign-in, a reauthentication or a change to how
// the account signs in. The new token is stored in that change's transaction, see storeSession,
// and only if the request then succeeds, so a failure after this point, such as a device token
// that could not be sealed, signs nothing in and changes nothing. The browser keeps the token it
// presented, and the session it loaded stays as it was.
func (s *Server) renewToken(ctx context.Context) error {
	if err := s.Sessions.RenewToken(ctx); err != nil {
		return err
	}
	ctx.Value(sessionLoadKey{}).(*sessionLoad).renewed = true
	return nil
}

// rotate replaces the token after a change to the account's password or two-factor
// authentication. Each such change takes a current credential, as reauthentication does, so
// recent authentication restarts, with two factors once the account has them; the session's
// absolute deadline stays, and a session that a logout has ended stays ended.
func (s *Server) rotate(ctx context.Context, a db.Account) error {
	ctx.Value(sessionLoadKey{}).(*sessionLoad).replaceLive = true
	deadline := s.Sessions.Deadline(ctx)
	if err := s.establish(ctx, a, len(a.TotpSecret) > 0); err != nil {
		return err
	}
	s.Sessions.SetDeadline(ctx, deadline)
	return nil
}

// storeSession is the Finish of a sign-in or of a change to how the account signs in. In the
// change's transaction it runs renew, which renews the session for the account if it needs to,
// sets *out to what the response shows of the session, and stores a renewed session there. The transaction
// then commits the change and the session together, before the response, so nothing may change
// the session after. A logout that ended the session first wins: the change rolls back with it.
func (s *Server) storeSession(ctx context.Context, out *api.Session, renew func(context.Context, db.Account) error) auth.Finish {
	return func(tx pgx.Tx, a db.Account) error {
		loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
		loaded.tx = tx
		defer func() { loaded.tx = nil }()
		if err := renew(ctx, a); err != nil {
			return err
		}
		var err error
		if *out, err = s.session(ctx, db.New(tx)); err != nil || !loaded.renewed {
			return err
		}
		if _, err = loaded.commit(ctx, s.Sessions); errors.Is(err, errSessionRevoked) {
			return domain.Unauthenticated
		} else if err != nil {
			return err
		}
		loaded.stored = true
		return nil
	}
}

// dropStaleSession ends a session whose account has moved on, without touching the
// browser's cookie: this request may have been overtaken by the credential change that
// renewed it, and a dead token grants nothing. keepRow also leaves the row for an account
// that still exists, because that change may still be replacing it and must not see it
// removed as if by a logout, and a token on an older account version grants nothing.
func (s *Server) dropStaleSession(ctx context.Context, keepRow bool) error {
	loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	loaded.keepCookie, loaded.keepRow = true, keepRow
	loaded.staleCSRF = s.Sessions.GetString(ctx, "csrf")
	return s.Sessions.Destroy(ctx)
}

// csrfToken returns the token a mutation must present. With no live session it derives
// from the cookie the browser already holds, so a request carrying a dead token can be
// answered without replacing a cookie that may have been renewed meanwhile. A session whose
// account moved on while its row remains keeps its stored token instead, which the page
// already holds, so it can still log out the renewal that a credential change is about to
// make. Either authorizes only what an anonymous session may do. Like a stored token it
// reaches only same-origin script, and knowing it requires the unguessable cookie, which
// __Host- stops another host planting; the hash keeps that cookie from script.
func (s *Server) csrfToken(ctx context.Context) string {
	token := s.Sessions.GetString(ctx, "csrf")
	loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if token != "" || s.Sessions.Token(ctx) != "" || loaded.presented == "" {
		return token
	}
	if loaded.staleCSRF != "" {
		return loaded.staleCSRF
	}
	derived := sha256.Sum256([]byte("astilba dead session csrf\x00" + loaded.presented))
	return base64.RawURLEncoding.EncodeToString(derived[:])
}

// credential is the current credential a request presents to confirm its holder's identity.
func credential(c api.Credential) auth.Credential {
	return auth.Credential{Password: value(c.Password), Code: value(c.Code)}
}

func (s *Server) Reauthenticate(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.Reauthenticate](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	ctx := r.Context()
	loaded := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	var out api.Session
	_, e = s.Auth.Reauthenticate(ctx, *principal(ctx), credential(b), holdTerminals(ctx, loaded), s.storeSession(ctx, &out, func(ctx context.Context, a db.Account) error {
		// Confirming credentials keeps open terminal sessions, unlike any other renewal. A
		// recovery code changes the account's version, which ends them, so there is nothing
		// to move.
		loaded.replaceLive = true
		loaded.moveTerminals = a.AuthVersion == principal(ctx).Account.AuthVersion
		nonce := s.Sessions.GetString(ctx, "github-session")
		if err := s.establish(ctx, a, len(a.TotpSecret) > 0); err != nil {
			return err
		}
		if nonce != "" {
			s.Sessions.Put(ctx, "github-session", nonce)
		}
		return nil
	}))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, out)
}
