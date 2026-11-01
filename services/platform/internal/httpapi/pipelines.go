package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

func (s *Server) ListGitConnections(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.Connections(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateGitConnection(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.GitConnectionRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	c := pipeline.Connection{Name: b.Name, Provider: string(b.Provider), BaseURL: b.BaseUrl, Repository: b.Repository}
	access := pipeline.Access{}
	if b.Api != nil {
		access.API = pipeline.Credential{Username: b.Api.Username, Password: pipelineString(b.Api.Password)}
	}
	if b.Clone != nil {
		access.Clone = pipeline.Credential{Username: b.Clone.Username, Password: pipelineString(b.Clone.Password)}
	}
	v, secret, e := s.Execution.CreateConnection(r.Context(), *principal(r.Context()), ws, c, access)
	s.executionResponse(w, r, 201, map[string]any{"connection": v, "webhookSecret": secret, "webhookUrl": s.Origin + "/hooks/git/" + v.ID.String()}, e)
}
func (s *Server) ListRegistries(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.Registries(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateRegistry(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.RegistryRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateRegistry(r.Context(), *principal(r.Context()), ws, pipeline.Registry{Name: b.Name, Repository: b.Repository}, pipeline.RegistryAccess{Push: pipeline.Credential{Username: b.Push.Username, Password: pipelineString(b.Push.Password)}, Pull: pipeline.Credential{Username: b.Pull.Username, Password: pipelineString(b.Pull.Password)}})
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) ListBuildSecrets(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.BuildSecrets(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateBuildSecret(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.BuildSecretRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateBuildSecret(r.Context(), *principal(r.Context()), ws, b.Name, pipelineString(b.Value))
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) GetGitSource(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.GitSource(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) BindGitSource(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.BindGitSource](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	var cfg pipeline.Source
	raw, _ := json.Marshal(b.Configuration)
	_ = json.Unmarshal(raw, &cfg)
	v, e := s.Execution.BindSource(r.Context(), *principal(r.Context()), ws, pid, env, b.BaseRevisionId, cfg)
	s.executionResponse(w, r, 202, v, e)
}
func (s *Server) RefreshGitSource(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.RefreshGitSource](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.RefreshSource(r.Context(), *principal(r.Context()), ws, pid, env, b.BaseRevisionId, b.DiscardDraft != nil && *b.DiscardDraft)
	s.executionResponse(w, r, 202, v, e)
}
func (s *Server) SubmitGitSource(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.SubmitGitSource](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.SubmitSource(r.Context(), *principal(r.Context()), ws, pid, env, b.BaseRevisionId, b.RequestKey)
	s.executionResponse(w, r, 202, v, e)
}
func (s *Server) ListPipelineRuns(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.PipelineRuns(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ListBuildRejections(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.BuildRejections(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateBuild(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.BuildRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	var cfg pipeline.BuildConfig
	raw, _ := json.Marshal(b.Configuration)
	_ = json.Unmarshal(raw, &cfg)
	v, e := s.Execution.CreateBuild(r.Context(), *principal(r.Context()), ws, pid, env, b.BaseRevisionId, b.ApplicationId, b.RequestKey, cfg)
	s.executionResponse(w, r, 202, v, e)
}
func (s *Server) PromoteArtifact(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.PromoteArtifact](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.PromoteArtifact(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) ReceiveGitPush(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	body, e := io.ReadAll(r.Body)
	if e == nil {
		e = s.Execution.ReceivePush(r.Context(), id, r.Header, body)
	}
	s.executionResponse(w, r, 202, nil, e)
}

func pipelineString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (s *Server) ListBuildArtifacts(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.Artifacts(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}

func (s *Server) UpdateGitSourcePolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.UpdateGitSourcePolicyJSONRequestBody](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	e = s.Execution.SourcePolicy(r.Context(), *principal(r.Context()), ws, pid, env, b.AutoDeploy)
	s.executionResponse(w, r, 204, nil, e)
}
