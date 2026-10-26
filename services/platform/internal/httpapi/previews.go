package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func (s *Server) GetPreviewPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Execution.PreviewPolicy(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) SavePreviewPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.SavePreviewPolicy](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.Execution.SavePreviewPolicy(r.Context(), *principal(r.Context()), ws, pid, env, b)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) ListPreviews(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Execution.Previews(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}
func (s *Server) ActOnPreview(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, err := decode[api.PreviewAction](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.Execution.ActOnPreview(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 200, v, err)
}
