package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

func (s *Server) GetTroubleshootingPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Execution.TroubleshootingPolicy(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) SaveTroubleshootingPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.SaveTroubleshootingPolicy](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.Execution.SaveTroubleshootingPolicy(r.Context(), *principal(r.Context()), ws, pid, env, b)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) ListOpenTroubleshootingSessions(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Execution.OpenTroubleshootingSessions(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) OpenTroubleshootingSession(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.OpenTroubleshootingSession](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	browser := execution.BrowserBinding{Token: s.Sessions.Token(r.Context()), Expires: s.Sessions.Deadline(r.Context())}
	v, err := s.Execution.OpenTroubleshootingSession(r.Context(), *principal(r.Context()), browser, ws, pid, env, b)
	s.executionResponse(w, r, 201, v, err)
}
func (s *Server) ListTroubleshootingSessions(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, err := s.Execution.TroubleshootingSessionHistory(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) CloseTroubleshootingSession(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	if err := s.Execution.CloseTroubleshootingSession(r.Context(), *principal(r.Context()), ws, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) CloseAllTroubleshootingSessions(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	if err := s.Execution.CloseAllTroubleshootingSessions(r.Context(), *principal(r.Context()), ws); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
