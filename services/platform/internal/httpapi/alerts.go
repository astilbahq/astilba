package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"net/http"
)

func (s *Server) ListAlerts(w http.ResponseWriter, r *http.Request, workspaceId uuid.UUID, params api.ListAlertsParams) {
	out, err := s.Alerts.Inbox(r.Context(), *principal(r.Context()), workspaceId, params.Before, params.View != nil && *params.View == "attention")
	s.executionResponse(w, r, 200, out, err)
}
func (s *Server) AcknowledgeAlert(w http.ResponseWriter, r *http.Request, workspaceId, alertId uuid.UUID) {
	err := s.Alerts.Acknowledge(r.Context(), *principal(r.Context()), workspaceId, alertId)
	s.executionResponse(w, r, 204, nil, err)
}
func (s *Server) GetAlertSettings(w http.ResponseWriter, r *http.Request, workspaceId uuid.UUID) {
	out, err := s.Alerts.Settings(r.Context(), *principal(r.Context()), workspaceId)
	s.executionResponse(w, r, 200, out, err)
}
func (s *Server) PutAlertSettings(w http.ResponseWriter, r *http.Request, workspaceId uuid.UUID) {
	in, err := decode[api.SaveAlertSettings](r)
	var out api.AlertSettings
	if err == nil {
		out, err = s.Alerts.SaveSettings(r.Context(), *principal(r.Context()), workspaceId, in)
	}
	s.executionResponse(w, r, 200, out, err)
}
