package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"net/http"
	"time"
)

func (s *Server) GetOnboarding(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	_, e := s.Execution.Servers(r.Context(), *principal(r.Context()), ws)
	out := api.Onboarding{ApplicationDomain: s.Store.ApplicationDomain, AutomaticDNS: s.Execution.DNS != nil, BootstrapURL: "/onboarding/bootstrap.sh"}
	out.KeyFingerprint = agentrelease.Fingerprint(s.AgentReleaseKey)
	s.executionResponse(w, r, 200, out, e)
}
func (s *Server) GetServerDomain(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	v, e := s.Execution.ServerDomain(r.Context(), *principal(r.Context()), ws, id)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ConfigureServerDomain(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.ConfigureServerDomain](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ConfigureServerDomain(r.Context(), *principal(r.Context()), ws, id, b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) SuggestApplicationDomain(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.SuggestApplicationDomainJSONBody](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.SuggestDomain(r.Context(), *principal(r.Context()), ws, pid, env, b.ApplicationId, b.ServerId)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CheckDomain(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.CheckDomainJSONBody](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	p := *principal(r.Context())
	if e = s.Auth.Limit(r.Context(), "dns-check:"+p.Account.ID.String(), 30, time.Minute); e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CheckDomain(r.Context(), p, ws, id, b.Hostname)
	s.executionResponse(w, r, 200, v, e)
}
