package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

func (s *Server) ListBackupDestinations(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.BackupDestinations(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateBackupDestination(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.BackupDestinationRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	d := backup.Destination{Name: b.Name, Kind: string(b.Kind), Root: b.Root, Endpoint: b.Endpoint, Bucket: b.Bucket, Region: b.Region}
	a := backup.Access{AccessKey: pipelineString(b.Access.AccessKey), SecretKey: pipelineString(b.Access.SecretKey)}
	v, e := s.Execution.CreateBackupDestination(r.Context(), *principal(r.Context()), ws, d, a)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) ListBackupPolicies(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.BackupPolicies(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateBackupPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	s.saveBackupPolicy(w, r, ws, pid, env, uuid.Nil)
}
func (s *Server) UpdateBackupPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	s.saveBackupPolicy(w, r, ws, pid, env, id)
}
func (s *Server) saveBackupPolicy(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.BackupPolicyRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.SaveBackupPolicy(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	status := 200
	if id == uuid.Nil {
		status = 201
	}
	s.executionResponse(w, r, status, v, e)
}
func (s *Server) GetBackupStatus(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	v, e := s.Execution.BackupStatus(r.Context(), *principal(r.Context()), ws, pid, env, id)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) RunBackupCommand(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.BackupCommandRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.BackupCommand(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 202, v, e)
}
func (s *Server) PrepareBackupCutover(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.BackupCutoverRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.PrepareBackupCutover(r.Context(), *principal(r.Context()), ws, pid, env, id, b)
	s.executionResponse(w, r, 200, revision(v), e)
}
func (s *Server) ReportSchedule(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.ScheduleReport](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	raw, _ := json.Marshal(b.Status)
	var status backup.Status
	if e = json.Unmarshal(raw, &status); e == nil {
		e = s.Execution.ScheduleReport(r.Context(), agentIdentity(r.Context()), b.Sequence, status)
	}
	s.executionResponse(w, r, 204, nil, e)
}
