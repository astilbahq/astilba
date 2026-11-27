package httpapi

import (
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// EnrollMFA starts enrolment. It needs no confirmation, because the authenticator does nothing
// until ConfirmMFA, which needs the password.
func (s *Server) EnrollMFA(w http.ResponseWriter, r *http.Request) {
	secret, uri, e := s.Auth.BeginMFA(r.Context(), *principal(r.Context()))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.MFAEnrollment{Secret: secret, Uri: uri})
}
func (s *Server) ConfirmMFA(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.MFAConfirmation](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	ctx := r.Context()
	var session api.Session
	a, codes, e := s.Auth.ConfirmMFA(ctx, *principal(ctx), credential(b.Current), b.Code, s.storeSession(ctx, &session, s.rotate))
	s.sendCodes(w, r, a, codes, session, e)
}
func (s *Server) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.CredentialConfirmation](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	ctx := r.Context()
	var session api.Session
	a, codes, e := s.Auth.RegenerateRecovery(ctx, *principal(ctx), credential(b.Current), s.storeSession(ctx, &session, s.rotate))
	s.sendCodes(w, r, a, codes, session, e)
}

// sendCodes shows new recovery codes, which were saved only with the session stored with them: a
// failure before then left the codes the account had, and nothing on the server can fail after.
func (s *Server) sendCodes(w http.ResponseWriter, r *http.Request, a db.Account, codes []string, session api.Session, e error) {
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.RecoveryCodes{Codes: codes, Session: session, Generation: a.RecoveryCodesGeneration})
}
func (s *Server) DisableMFA(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.CredentialConfirmation](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	ctx := r.Context()
	var out api.Session
	_, e = s.Auth.DisableMFA(ctx, *principal(ctx), credential(b.Current), s.storeSession(ctx, &out, s.rotate))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, out)
}

func (s *Server) AcknowledgeRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.RecoveryCodesAcknowledgement](r)
	if e == nil {
		e = s.Auth.AcknowledgeRecoveryCodes(r.Context(), *principal(r.Context()), b.Generation)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.sendSession(w, r)
}
