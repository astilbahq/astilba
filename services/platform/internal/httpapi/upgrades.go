package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"net/http"
)

func (s *Server) ListUpgrades(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.Upgrades(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateUpgrade(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.UpgradeRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateUpgrade(r.Context(), *principal(r.Context()), ws, pid, env, b)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) RunUpgradeAction(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.UpgradeActionRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.UpgradeAction(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 202, v, e)
}
