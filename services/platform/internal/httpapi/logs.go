package httpapi

import (
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func (s *Server) ExportLogs(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID, params api.ExportLogsParams) {
	hours := 1
	if params.Hours != nil {
		hours = int(*params.Hours)
	}
	out, err := s.Execution.ExportLogs(r.Context(), *principal(r.Context()), ws, pid, env, hours)
	if err == nil {
		w.Header().Set("Content-Disposition", `attachment; filename="astilba-logs.json"`)
	}
	s.executionResponse(w, r, http.StatusOK, out, err)
}
