package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"

	hostassets "github.com/astilbahq/astilba/services/platform/host"
	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
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
	browser := s.browserSession(s.identity(s.csrf(next)))
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
			if len(r.Header.Values("Authorization")) != 0 {
				s.automationRequest(next).ServeHTTP(w, r)
				return
			}
			browser.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		w.Header().Set("Astilba-Agent-Build-Cache", "v1")
		w.Header().Set("Astilba-Agent-Railpack", "v1")
		w.Header().Set("Astilba-Agent-Safe-Releases", "v1")
		w.Header().Set("Astilba-Agent-Service-Outputs", "v1")
		w.Header().Set("Astilba-Agent-Previews", "v1")
		w.Header().Set("Astilba-Agent-Schedules", "v1")
		w.Header().Set("Astilba-Agent-Replicas", "v1")
		w.Header().Set("Astilba-Agent-Cross-Host", "v1")
		w.Header().Set("Astilba-Agent-Maintenance-Continuation", "v1")
		w.Header().Set("Astilba-Agent-Primary-Ingress", "v1")
		// v1 receipts may say why a host refused a network command.
		w.Header().Set("Astilba-Agent-Network-Refusals", "v1")
		// v2 adds session streams; agents advertise support only when they can run them.
		w.Header().Set("Astilba-Agent-Troubleshooting", "v2")
		limit := int64(16384)
		if r.URL.Path == "/agent/v1/enroll" {
			limit = 32768
		}
		if r.URL.Path == "/agent/v1/report" || r.URL.Path == "/agent/v1/schedule-reports" {
			limit = 196608
		}
		if r.URL.Path == "/agent/v1/report" {
			limit = 524288
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if r.URL.Path != "/agent/v1/enroll" {
			identity, e := s.agentRequestIdentity(r)
			if e != nil {
				s.fail(w, r, e)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), agentKey, identity))
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) agentRequestIdentity(r *http.Request) (execution.Identity, error) {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 2 {
		return execution.Identity{}, domain.Unauthenticated
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return execution.Identity{}, domain.Unauthenticated
	}
	return s.Execution.Authenticate(r.Context(), id, parts[1])
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
	if e := s.Auth.Limit(r.Context(), "agent-enroll:"+s.clientSourceKey(r), 30, execution.RotationOverlap); e != nil {
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
	v, e := s.Execution.PollAgent(r.Context(), agentIdentity(r.Context()))
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) DownloadRecoveryCheckpoint(w http.ResponseWriter, r *http.Request, parentID, checkpointID uuid.UUID) {
	body, err := s.Execution.RecoveryCheckpoint(r.Context(), agentIdentity(r.Context()), parentID, checkpointID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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

// agentRelease finds the loaded release a request names by digest. Handlers use the release's
// own fields from here on, never the request's text.
func (s *Server) agentRelease(r *http.Request) (api.AgentRelease, bool) {
	sha := chi.URLParam(r, "sha")
	for _, release := range s.Execution.Releases {
		if release.Sha256 == sha {
			return release, true
		}
	}
	return api.AgentRelease{}, false
}

func (s *Server) agentArtifact(w http.ResponseWriter, r *http.Request) {
	release, ok := s.agentRelease(r)
	if !ok || s.ArtifactDirectory == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, agentrelease.ArtifactPath(s.ArtifactDirectory, release))
}

func (s *Server) agentManifest(w http.ResponseWriter, r *http.Request) {
	release, ok := s.agentRelease(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.respond(w, 200, release)
}

func (s *Server) agentReleaseMessage(w http.ResponseWriter, r *http.Request) {
	release, ok := s.agentRelease(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(agentrelease.Message(release))
}

func (s *Server) agentReleaseSignature(w http.ResponseWriter, r *http.Request) {
	release, ok := s.agentRelease(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(release.Signature)
	if err != nil || len(raw) != 64 {
		http.Error(w, "Release signature unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(raw)
}

func (s *Server) agentPublicKey(w http.ResponseWriter, r *http.Request) {
	if len(s.AgentReleaseKey) != ed25519.PublicKeySize {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/x-pem-file")
	der, _ := x509.MarshalPKIXPublicKey(s.AgentReleaseKey)
	_, _ = w.Write(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func (s *Server) hostBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="astilba-bootstrap.sh"`)
	_, _ = w.Write(hostassets.Bootstrap())
}
