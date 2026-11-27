package httpapi

import (
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// sessionsListed is how many of the account's sessions the account page shows: the contract's
// limit. Signing out every other session ends any beyond them too.
const sessionsListed = 50

func (s *Server) ListAccountSessions(w http.ResponseWriter, r *http.Request) {
	s.sendAccountSessions(w, r)
}

func (s *Server) SignOutSession(w http.ResponseWriter, r *http.Request, sessionId uuid.UUID) {
	if e := s.Auth.SignOutSessions(r.Context(), *principal(r.Context()), &sessionId); e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendAccountSessions(w, r)
}

func (s *Server) SignOutOtherSessions(w http.ResponseWriter, r *http.Request) {
	if e := s.Auth.SignOutSessions(r.Context(), *principal(r.Context()), nil); e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendAccountSessions(w, r)
}

// sendAccountSessions answers with the account's signed-in sessions, marking the one that asks.
func (s *Server) sendAccountSessions(w http.ResponseWriter, r *http.Request) {
	p := principal(r.Context())
	current := r.Context().Value(sessionLoadKey{}).(*sessionLoad).lineage
	rows, e := s.Store.Queries.AccountSessions(r.Context(), db.AccountSessionsParams{AccountID: p.Account.ID, Current: current, Listed: sessionsListed})
	if e != nil {
		s.fail(w, r, e)
		return
	}
	out := api.AccountSessions{Sessions: make([]api.AccountSession, 0, len(rows))}
	for _, row := range rows {
		out.Unlisted = int(row.Live) - len(rows)
		session := api.AccountSession{Id: row.Lineage, Current: row.Lineage == current, SignedInAt: row.SignedInAt, LastSeenAt: row.LastSeenAt, Browser: api.AccountSessionBrowser(row.ClientBrowser.String), Os: api.AccountSessionOs(row.ClientOs.String)}
		// A later release may record a family this one does not name, and survive a rollback.
		if !session.Browser.Valid() {
			session.Browser = api.AccountSessionBrowserOther
		}
		if !session.Os.Valid() {
			session.Os = api.AccountSessionOsOther
		}
		if row.ClientNetwork.Valid {
			session.Network = &row.ClientNetwork.String
		}
		out.Sessions = append(out.Sessions, session)
	}
	s.respond(w, http.StatusOK, out)
}
