package httpapi

import (
	"context"
	"net/http"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"
)

func (s *Server) automationRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		values := r.Header.Values("Authorization")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(values) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || publicPath(r.URL.Path) {
			s.fail(w, r, domain.Unauthenticated)
			return
		}
		for _, cookie := range r.Cookies() {
			if cookie.Name == "astilba_session" || cookie.Name == "__Host-astilba_session" {
				s.fail(w, r, domain.Forbidden)
				return
			}
		}
		p, err := s.Store.AuthenticateAutomation(r.Context(), parts[1])
		if err != nil {
			s.fail(w, r, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 150000)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, &p)))
	})
}

func (s *Server) authenticateAPI(ctx context.Context, in *openapi3filter.AuthenticationInput) error {
	p := principal(ctx)
	switch in.SecuritySchemeName {
	case "agentBearer":
		if agentIdentity(ctx).ID != uuid.Nil {
			return nil
		}
	case "sessionCookie":
		if p != nil && p.Automation == nil {
			return s.authorizeEnvironmentPurpose(ctx, in.RequestValidationInput)
		}
	case "automationBearer":
		if p != nil && p.Automation != nil {
			if err := authorizeAutomation(*p.Automation, in.RequestValidationInput.Route.Operation.OperationID, in.RequestValidationInput.PathParams); err != nil {
				return err
			}
			return s.authorizeEnvironmentPurpose(ctx, in.RequestValidationInput)
		}
	}
	return domain.Unauthenticated
}

// Keep this allowlist independent of browser membership: a token never gains a
// new API operation merely because its creator can use that operation.
func authorizeAutomation(g domain.AutomationGrant, operation string, path map[string]string) error {
	if operation == "GetAutomationContext" {
		return nil
	}
	for key, expected := range map[string]uuid.UUID{"workspaceId": g.WorkspaceID, "projectId": g.ProjectID, "environmentId": g.EnvironmentID} {
		id, err := uuid.Parse(path[key])
		if err != nil || id != expected {
			return domain.Forbidden
		}
	}
	var capabilities []string
	switch operation {
	case "GetEnvironment", "GetDraft", "ListRevisions", "GetRevision", "GetRuntime", "ListPipelineRuns", "ListBuildRejections", "ListReleases":
		capabilities = []string{domain.EnvironmentRead}
	case "UpdateDraft":
		capabilities = []string{domain.ConfigurationWrite}
	case "CreateVisualPlan":
		capabilities = []string{domain.DeploymentPlan}
	case "GetVisualPlan":
		capabilities = []string{domain.EnvironmentRead, domain.DeploymentPlan, domain.DeploymentApply}
	case "ApplyVisualPlan":
		capabilities = []string{domain.DeploymentApply}
	case "ListConfiguredBuildTargets":
		capabilities = []string{domain.EnvironmentRead, domain.BuildRun}
	case "RunConfiguredBuild":
		capabilities = []string{domain.BuildRun}
	case "GetEnvironmentOperation":
		capabilities = []string{domain.EnvironmentRead, domain.DeploymentPlan, domain.DeploymentApply, domain.BuildRun}
	default:
		return domain.Forbidden
	}
	if !g.Allows(capabilities...) {
		return domain.Forbidden
	}
	return nil
}

func (s *Server) CreateAutomationCredential(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, err := decode[api.CreateAutomationCredential](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	caps := make([]string, len(b.Capabilities))
	for i, c := range b.Capabilities {
		caps[i] = string(c)
	}
	v, token, err := s.Store.CreateAutomation(r.Context(), *principal(r.Context()), ws, pid, env, b.Name, caps, b.ExpiresAt)
	s.executionResponse(w, r, 201, map[string]any{"credential": v, "token": token}, err)
}

func (s *Server) ListAutomationCredentials(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Store.ListAutomation(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}

func (s *Server) RevokeAutomationCredential(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	err := s.Store.RevokeAutomation(r.Context(), *principal(r.Context()), ws, pid, env, id)
	s.executionResponse(w, r, 204, nil, err)
}

func (s *Server) GetAutomationContext(w http.ResponseWriter, r *http.Request) {
	p := *principal(r.Context())
	g := p.Automation
	if g == nil {
		s.fail(w, r, domain.Forbidden)
		return
	}
	var out api.AutomationContext
	err := s.Store.InWorkspace(r.Context(), p, g.WorkspaceID, false, false, false, func(q *db.Queries, workspace store.Workspace) error {
		env, err := q.GetEnvironment(r.Context(), db.GetEnvironmentParams{WorkspaceID: g.WorkspaceID, ProjectID: g.ProjectID, ID: g.EnvironmentID})
		if err != nil {
			return err
		}
		project, err := q.GetProject(r.Context(), db.GetProjectParams{WorkspaceID: g.WorkspaceID, ID: g.ProjectID})
		if err != nil {
			return err
		}
		caps := make([]api.AutomationCapability, len(g.Capabilities))
		for i, c := range g.Capabilities {
			caps[i] = api.AutomationCapability(c)
		}
		out = api.AutomationContext{CredentialId: g.ID, WorkspaceId: g.WorkspaceID, ProjectId: g.ProjectID, EnvironmentId: g.EnvironmentID, Workspace: workspace.Name, Project: project.Name, Environment: env.Name, Capabilities: caps, ExpiresAt: g.ExpiresAt}
		return nil
	})
	s.executionResponse(w, r, 200, out, err)
}

func (s *Server) GetEnvironmentOperation(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	v, err := s.Execution.EnvironmentOperation(r.Context(), *principal(r.Context()), ws, pid, env, id)
	s.executionResponse(w, r, 200, v, err)
}

func (s *Server) ListConfiguredBuildTargets(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, err := s.Execution.ConfiguredBuildTargets(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, err)
}

func (s *Server) RunConfiguredBuild(w http.ResponseWriter, r *http.Request, ws, pid, env, app uuid.UUID) {
	b, err := decode[api.RunConfiguredBuild](r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.Execution.RunConfiguredBuild(r.Context(), *principal(r.Context()), ws, pid, env, app, b)
	s.executionResponse(w, r, 202, v, err)
}
