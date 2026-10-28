package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"net/http"
)

func (s *Server) GetManagedNetwork(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.ManagedNetwork(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ConfigureManagedNetwork(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.ConfigureManagedNetwork](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ConfigureManagedNetwork(r.Context(), *principal(r.Context()), ws, b)
	s.executionResponse(w, r, 200, v, e)
}
