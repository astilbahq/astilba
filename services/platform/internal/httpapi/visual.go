package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func (s *Server) CreateVisualPlan(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.VisualPlanRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateVisualPlan(r.Context(), *principal(r.Context()), ws, pid, env, b)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) GetVisualPlan(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	v, e := s.Execution.VisualPlan(r.Context(), *principal(r.Context()), ws, pid, env, id)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ApplyVisualPlan(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.ApplyPlan](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ApplyVisualPlan(r.Context(), *principal(r.Context()), ws, pid, env, id, b.IdempotencyKey)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) GetRuntime(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.Runtime(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) RefreshRuntime(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.ObserveVisual(r.Context(), *principal(r.Context()), ws, pid, env, nil)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) DeleteVolume(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	v, e := s.Execution.ObserveVisual(r.Context(), *principal(r.Context()), ws, pid, env, &id)
	s.executionResponse(w, r, 201, v, e)
}

func (s *Server) CloneEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.CloneEnvironment](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CloneVisual(r.Context(), *principal(r.Context()), ws, pid, env, b)
	s.executionResponse(w, r, 201, environment(v), e)
}
func (s *Server) ExportEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.ExportVisual(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}

func (s *Server) ListManagedVolumes(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.ManagedVolumes(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}

func (s *Server) ListReleases(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.ReleaseHistory(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) RollbackRelease(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.RollbackRelease](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.RollbackRelease(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 201, v, e)
}
