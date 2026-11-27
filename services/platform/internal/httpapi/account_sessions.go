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
	rows, e := s.Store.Queries.AccountSessions(r.Context(), db.AccountSessionsParams{AccountID: p.Account.ID, Listed: sessionsListed})
	if e != nil {
		s.fail(w, r, e)
		return
	}
	current := r.Context().Value(sessionLoadKey{}).(*sessionLoad).lineage
	out := api.AccountSessions{Sessions: make([]api.AccountSession, 0, len(rows))}
	for _, row := range rows {
		out.Unlisted = int(row.Live) - len(rows)
		if row.SignedInAt == nil || row.LastSeenAt == nil {
			// A signed-in row has both, which the table's check keeps true.
			continue
		}
		session := api.AccountSession{Id: row.Lineage, Current: row.Lineage == current, SignedInAt: *row.SignedInAt, LastSeenAt: *row.LastSeenAt, Browser: api.AccountSessionBrowser(row.ClientBrowser.String), Os: api.AccountSessionOs(row.ClientOs.String)}
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
