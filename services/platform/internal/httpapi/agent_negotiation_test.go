package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexedwards/scs/v2"
)

// Agents tell a controller's response from a reverse proxy's by this header, so
// a rejection has to carry it as well as a success.
func TestAgentRejectionIdentifiesController(t *testing.T) {
	s := &Server{Sessions: scs.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/heartbeat", nil)
	response := httptest.NewRecorder()
	s.request(s.protection(http.NotFoundHandler())).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("Astilba-Agent-Features") == "" {
		t.Fatal("rejection does not identify the controller")
	}
}
