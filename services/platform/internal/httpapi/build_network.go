package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func (s *Server) GetServerBuildNetwork(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	v, e := s.Execution.ServerBuildNetwork(r.Context(), *principal(r.Context()), ws, id)
	s.executionResponse(w, r, 200, v, e)
}

func (s *Server) ConfigureServerBuildNetwork(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.BuildNetwork](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ConfigureServerBuildNetwork(r.Context(), *principal(r.Context()), ws, id, b)
	s.executionResponse(w, r, 200, v, e)
}
