package httpapi

import (
	"context"
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Server) RetireEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	s.changeLifecycle(w, r, ws, pid, env, s.Execution.RetireEnvironment)
}

func (s *Server) RestoreEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	s.changeLifecycle(w, r, ws, pid, env, s.Execution.RestoreEnvironment)
}

func (s *Server) PurgeEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.EnvironmentPurgeRequest](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = s.Execution.PurgeEnvironment(r.Context(), *principal(r.Context()), ws, pid, env, b.ConfirmName, b.VolumeIds); err != nil {
		s.fail(w, r, err)
		return
	}
	s.GetEnvironment(w, r, ws, pid, env)
}

// changeLifecycle answers with the environment as it is after the change.
func (s *Server) changeLifecycle(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID, change func(context.Context, store.Principal, uuid.UUID, uuid.UUID, uuid.UUID, string) error) {
	b, err := decode[api.EnvironmentLifecycleRequest](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = change(r.Context(), *principal(r.Context()), ws, pid, env, b.ConfirmName); err != nil {
		s.fail(w, r, err)
		return
	}
	s.GetEnvironment(w, r, ws, pid, env)
}
