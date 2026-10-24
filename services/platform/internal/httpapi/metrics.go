package httpapi

import (
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"net/http"
)

func (s *Server) ReportAgentMetrics(w http.ResponseWriter, r *http.Request) {
	in, err := decode[api.MetricReport](r)
	if err == nil {
		err = s.Execution.RecordMetrics(r.Context(), agentIdentity(r.Context()), in)
	}
	s.executionResponse(w, r, 204, nil, err)
}
func (s *Server) GetServerMetrics(w http.ResponseWriter, r *http.Request, workspaceId, serverId uuid.UUID, params api.GetServerMetricsParams) {
	hours := 1
	if params.Hours != nil {
		hours = int(*params.Hours)
	}
	out, err := s.Execution.Metrics(r.Context(), *principal(r.Context()), workspaceId, serverId, hours)
	s.executionResponse(w, r, 200, out, err)
}
