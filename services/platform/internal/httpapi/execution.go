package httpapi

import (
	"context"
	"github.com/go-chi/chi/v5"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

const agentKey contextKey = 2

func agentIdentity(ctx context.Context) execution.Identity {
	v, _ := ctx.Value(agentKey).(execution.Identity)
	return v
}
func (s *Server) protection(next http.Handler) http.Handler {
	browser := s.Sessions.LoadAndSave(s.identity(s.csrf(next)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hooks/git/") || r.URL.Path == "/hooks/stripe" || r.URL.Path == "/hooks/github-app" {
			w.Header().Set("Cache-Control", "no-store")
			limit := int64(1 << 20)
			if r.URL.Path == "/hooks/github-app" {
				limit = 150000
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/agent/v1/") {
			browser.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		w.Header().Set("Astilba-Agent-Build-Cache", "v1")
		w.Header().Set("Astilba-Agent-Railpack", "v1")
		w.Header().Set("Astilba-Agent-Safe-Releases", "v1")
		w.Header().Set("Astilba-Agent-Service-Outputs", "v1")
		limit := int64(16384)
		if r.URL.Path == "/agent/v1/report" || r.URL.Path == "/agent/v1/schedule-reports" {
			limit = 196608
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if r.URL.Path != "/agent/v1/enroll" {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			parts := strings.Split(value, ".")
			if len(parts) != 2 {
				s.fail(w, r, domain.Unauthenticated)
				return
			}
			id, e := uuid.Parse(parts[0])
			if e != nil {
				s.fail(w, r, domain.Unauthenticated)
				return
			}
			identity, e := s.Execution.Authenticate(r.Context(), id, parts[1])
			if e != nil {
				s.fail(w, r, e)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), agentKey, identity))
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) ListServers(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.Servers(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateServer(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.CreateServer](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateServer(r.Context(), *principal(r.Context()), ws, b)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) RenewEnrollment(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	v, e := s.Execution.RenewEnrollment(r.Context(), *principal(r.Context()), ws, id)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) RequestRotation(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	e := s.Execution.Control(r.Context(), *principal(r.Context()), ws, id, false)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) RevokeServer(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	e := s.Execution.Control(r.Context(), *principal(r.Context()), ws, id, true)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) ListAgentReleases(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	_, e := s.Execution.Servers(r.Context(), *principal(r.Context()), ws)
	v := s.Execution.Releases
	if v == nil {
		v = []api.AgentRelease{}
	}
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) UpdateAgent(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.UpdateAgent](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.RequestUpdate(r.Context(), *principal(r.Context()), ws, id, b.Version)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) CreateExecutionPlan(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.PlanRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.Plan(r.Context(), *principal(r.Context()), ws, b)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) ApplyExecutionPlan(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.ApplyPlan](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.Apply(r.Context(), *principal(r.Context()), ws, id, b.IdempotencyKey)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) ListOperations(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.Operations(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CancelOperation(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	e := s.Execution.Cancel(r.Context(), *principal(r.Context()), ws, id)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) EnrollAgent(w http.ResponseWriter, r *http.Request) {
	if e := s.Auth.Limit(r.Context(), "agent-enroll:"+s.clientIP(r), 30, execution.RotationOverlap); e != nil {
		s.fail(w, r, e)
		return
	}
	b, e := decode[api.AgentEnrollment](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.Enroll(r.Context(), b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) AgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.AgentHeartbeat](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.Heartbeat(r.Context(), agentIdentity(r.Context()), b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) RotateAgentCredential(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.AgentRotation](r)
	if e == nil {
		e = s.Execution.Rotate(r.Context(), agentIdentity(r.Context()), b.Credential)
	}
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) PollAgent(w http.ResponseWriter, r *http.Request) {
	v, e := s.Execution.Poll(r.Context(), agentIdentity(r.Context()))
	s.executionResponse(w, r, 200, api.AgentPoll{Command: v}, e)
}
func (s *Server) ReportAgent(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.AgentReport](r)
	if e == nil {
		e = s.Execution.Report(r.Context(), agentIdentity(r.Context()), b)
	}
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) executionResponse(w http.ResponseWriter, r *http.Request, status int, v any, e error) {
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, status, v)
}

func (s *Server) agentArtifact(w http.ResponseWriter, r *http.Request) {
	sha := chi.URLParam(r, "sha")
	for _, release := range s.Execution.Releases {
		if release.Sha256 == sha && s.ArtifactDirectory != "" {
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, filepath.Join(s.ArtifactDirectory, sha))
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) agentManifest(w http.ResponseWriter, r *http.Request) {
	for _, release := range s.Execution.Releases {
		if release.Sha256 == chi.URLParam(r, "sha") {
			s.respond(w, 200, release)
			return
		}
	}
	http.NotFound(w, r)
}
