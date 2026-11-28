package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func (s *Server) ListBackupStorageAccounts(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	v, e := s.Execution.BackupStorageAccounts(r.Context(), *principal(r.Context()), ws)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CreateBackupStorageAccount(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.BackupStorageAccountRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.CreateBackupStorageAccount(r.Context(), *principal(r.Context()), ws, b)
	s.executionResponse(w, r, 201, v, e)
}
func (s *Server) ReplaceBackupStorageAccountCredential(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID) {
	b, e := decode[api.BackupStorageCredentialRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.ReplaceBackupStorageAccountCredential(r.Context(), *principal(r.Context()), ws, account, b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) CheckBackupStorageAccount(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID) {
	e := s.Execution.CheckBackupStorageAccount(r.Context(), *principal(r.Context()), ws, account)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) AddBackupStorageAccountPeer(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID) {
	b, e := decode[api.BackupStorageAccountPeerRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	e = s.Execution.AddBackupStorageAccountPeer(r.Context(), *principal(r.Context()), ws, account, b)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) RevokeFoundBackupStorageUser(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID, user string) {
	if e := urlNUL(r); e != nil {
		s.fail(w, r, e)
		return
	}
	e := s.Execution.RevokeFoundBackupStorageUser(r.Context(), *principal(r.Context()), ws, account, user)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) RevokeBackupStorageKey(w http.ResponseWriter, r *http.Request, ws, destination, key uuid.UUID) {
	e := s.Execution.RevokeBackupStorageKey(r.Context(), *principal(r.Context()), ws, destination, key)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) RotateBackupStorageKey(w http.ResponseWriter, r *http.Request, ws, destination uuid.UUID) {
	e := s.Execution.RotateBackupStorageKey(r.Context(), *principal(r.Context()), ws, destination)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) SetBackupStorageKeyRotation(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID) {
	b, e := decode[api.BackupStorageKeyRotationRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v, e := s.Execution.SetBackupStorageKeyRotation(r.Context(), *principal(r.Context()), ws, account, b)
	s.executionResponse(w, r, 200, v, e)
}
func (s *Server) RetryBackupStorageKeys(w http.ResponseWriter, r *http.Request, ws, destination uuid.UUID) {
	e := s.Execution.RetryBackupStorageKeys(r.Context(), *principal(r.Context()), ws, destination)
	s.executionResponse(w, r, 204, nil, e)
}
func (s *Server) AcknowledgeFoundBackupStorageUser(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID, user string) {
	if e := urlNUL(r); e != nil {
		s.fail(w, r, e)
		return
	}
	b, e := decode[api.BackupStorageAcknowledgementRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	e = s.Execution.AcknowledgeFoundBackupStorageUser(r.Context(), *principal(r.Context()), ws, account, user, b)
	s.executionResponse(w, r, 204, nil, e)
}
