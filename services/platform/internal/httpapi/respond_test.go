package httpapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// API clients read these bytes, so a change here is a contract change.
func TestResponsesKeepTheirFormat(t *testing.T) {
	type body struct {
		Empty  []string `json:"empty"`
		Absent int      `json:"absent,omitempty"`
		Text   string   `json:"text"`
	}
	w := httptest.NewRecorder()
	(&Server{}).respond(w, 201, body{Text: "a&b"})
	want := `{"empty":null,"text":"a` + `\` + "u0026b\"}\n"
	if w.Code != 201 || w.Body.String() != want || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestRespondWithoutBodySendsOnlyTheStatus(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).respond(w, 204, nil)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestRespondReportsAValueThatCannotBeEncoded(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "request")
	(&Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).respond(w, 200, jsontext.Value(`{"truncated"`))
	var got api.Error
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	if w.Code != 500 || got.Code != "internal_error" || got.RequestId != "request" {
		t.Fatalf("%d %+v", w.Code, got)
	}
}
