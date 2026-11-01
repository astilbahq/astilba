package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"io"
	"net/http"
)

func (s *Server) githubSession(r *http.Request) string {
	return s.Sessions.GetString(r.Context(), "github-session")
}
func (s *Server) GetGitHubApp(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.GitHubStatus(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) StartGitHubAuthorization(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.StartGitHub(r.Context(), *principal(r.Context()), ws, s.githubSession(r))

	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CompleteGitHubAuthorization(w http.ResponseWriter, r *http.Request) {
	in, e := decode[api.GitHubAuthorizationComplete](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CompleteGitHub(r.Context(), *principal(r.Context()), s.githubSession(r), in.State, in.Code)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ListGitHubInstallations(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID, params api.ListGitHubInstallationsParams) {
	page := 1
	if params.Page != nil {
		page = *params.Page
	}
	values, e := s.Execution.GitHubInstallations(r.Context(), *principal(r.Context()), ws, id, s.githubSession(r), page)
	out := []api.GitHubInstallation{}
	for _, v := range values {
		dto := api.GitHubInstallation{Id: v.ID, Available: s.Execution.GitHub != nil && v.AppID == s.Execution.GitHub.Config.ID && v.SuspendedAt == nil}
		dto.Account.Login = v.Account.Login
		out = append(out, dto)
	}
	s.executionResponse(w, r, 200, out, e)
}
func (s *Server) ListGitHubRepositories(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID, params api.ListGitHubRepositoriesParams) {
	page := 1
	if params.Page != nil {
		page = *params.Page
	}
	values, e := s.Execution.GitHubRepositories(r.Context(), *principal(r.Context()), ws, id, s.githubSession(r), params.InstallationId, page)
	out := []api.GitHubRepository{}
	for _, v := range values {
		dto := api.GitHubRepository{Id: v.ID, FullName: v.FullName, Private: v.Private}
		dto.Permissions.Admin = v.Permissions.Admin
		out = append(out, dto)
	}
	s.executionResponse(w, r, 200, out, e)
}
func (s *Server) ConnectGitHubRepository(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	in, e := decode[api.GitHubConnect](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ConnectGitHub(r.Context(), *principal(r.Context()), ws, id, s.githubSession(r), execution.GitHubConnect{Name: in.Name, Repository: in.Repository, RepositoryID: in.RepositoryId, InstallationID: in.InstallationId, ConnectionID: in.ConnectionId})
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) DisconnectGitHubRepository(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	e := s.Execution.DisconnectGitHub(r.Context(), *principal(r.Context()), ws, id)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) ReceiveGitHubApp(w http.ResponseWriter, r *http.Request) {
	body, e := io.ReadAll(r.Body)
	if e == nil {
		e = s.Execution.ReceiveGitHub(r.Context(), r.Header, body)
	}
	s.executionResponse(w, r, 202, nil, e)
}
