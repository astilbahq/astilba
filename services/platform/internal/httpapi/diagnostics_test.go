package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const diagnosticCanary = "private-diagnostic-canary"

func TestFailureDiagnosticsExcludeRequestAndErrorContents(t *testing.T) {
	for _, tc := range []struct {
		name, category, sqlstate string
		err                      error
		status                   int
	}{
		{name: "unknown", category: "internal", err: errors.New(diagnosticCanary)},
		{name: "canceled", category: "context_canceled", err: fmt.Errorf("%s: %w", diagnosticCanary, context.Canceled)},
		{name: "deadline", category: "context_deadline_exceeded", err: fmt.Errorf("%s: %w", diagnosticCanary, context.DeadlineExceeded)},
		{name: "database", category: "database", sqlstate: "40001", err: fmt.Errorf("%s: %w", diagnosticCanary, &pgconn.PgError{Code: "40001", Message: diagnosticCanary, Detail: diagnosticCanary, Hint: diagnosticCanary, InternalQuery: diagnosticCanary, TableName: diagnosticCanary})},
		{name: "invalid database code", category: "database", err: &pgconn.PgError{Code: diagnosticCanary, Message: diagnosticCanary}},
		{name: "network timeout", category: "network_timeout", err: &url.Error{Op: "Get", URL: diagnosticCanary, Err: &net.DNSError{Err: diagnosticCanary, Name: diagnosticCanary, IsTimeout: true}}},
		{name: "network failure", category: "network", err: &net.DNSError{Err: diagnosticCanary, Name: diagnosticCanary}},
		{name: "not found", err: pgx.ErrNoRows, status: http.StatusNotFound},
		{name: "conflict", err: &pgconn.PgError{Code: "23505", Detail: diagnosticCanary}, status: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, records := diagnosticRequest(t, func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.fail(w, r, tc.err)
			})
			if tc.status != 0 {
				if response.Code != tc.status || len(records) != 1 || records[0]["level"] != "INFO" {
					t.Fatalf("expected domain response without error log: status %d, records %v", response.Code, records)
				}
				return
			}
			assertInternalResponse(t, response)
			if len(records) != 2 || records[0]["level"] != "ERROR" || records[1]["level"] != "INFO" {
				t.Fatalf("expected one failure and one request record, got %v", records)
			}
			record := records[0]
			if record["msg"] != "request failed" {
				t.Fatalf("unexpected error record: %v", record)
			}
			diagnostic := record["error"].(map[string]any)
			if diagnostic["category"] != tc.category {
				t.Errorf("category = %v, want %s", diagnostic["category"], tc.category)
			}
			if tc.sqlstate == "" {
				if _, ok := diagnostic["sqlstate"]; ok {
					t.Errorf("unexpected SQLSTATE: %v", diagnostic)
				}
			} else if diagnostic["sqlstate"] != tc.sqlstate {
				t.Errorf("SQLSTATE = %v, want %s", diagnostic["sqlstate"], tc.sqlstate)
			}
		})
	}
}

func TestPanicDiagnosticsRetainSourceWithoutPanicContents(t *testing.T) {
	for _, value := range []any{diagnosticCanary, errors.New(diagnosticCanary), diagnosticPanicValue{}} {
		response, records := diagnosticRequest(t, func(_ *Server, _ http.ResponseWriter, _ *http.Request) {
			panicDiagnostic(value)
		})
		assertInternalResponse(t, response)
		if len(records) != 1 || records[0]["msg"] != "request panic" || records[0]["level"] != "ERROR" {
			t.Fatalf("expected one panic record, got %v", records)
		}
		record := records[0]
		if record["error"].(map[string]any)["category"] != "panic" {
			t.Fatalf("unexpected panic category: %v", record)
		}
		frames := record["frames"].([]any)
		if len(frames) == 0 || len(frames) > 32 {
			t.Fatalf("expected bounded panic frames, got %v", frames)
		}
		found := false
		for _, raw := range frames {
			frame := raw.(map[string]any)
			file := frame["file"].(string)
			if strings.ContainsAny(file, `/\`) || file == "" || frame["line"].(float64) <= 0 {
				t.Errorf("invalid sanitized source location: %v", frame)
			}
			if strings.HasSuffix(frame["function"].(string), ".panicDiagnostic") && file == "diagnostics_test.go" {
				found = true
			}
		}
		if !found {
			t.Fatalf("panic origin absent from source frames: %v", frames)
		}
	}
}

type diagnosticPanicValue struct{}

func (diagnosticPanicValue) String() string { panic(diagnosticCanary) }

func panicDiagnostic(value any) { panic(value) }

func diagnosticRequest(t *testing.T, handler func(*Server, http.ResponseWriter, *http.Request)) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	router := chi.NewRouter()
	router.Use(s.request)
	router.Post("/diagnostics/{id}", func(w http.ResponseWriter, r *http.Request) { handler(s, w, r) })
	request := httptest.NewRequest(http.MethodPost, "http://api.example.test/diagnostics/"+diagnosticCanary+"?token="+diagnosticCanary, strings.NewReader(diagnosticCanary))
	request.Header.Set("Authorization", "Bearer "+diagnosticCanary)
	request.Header.Set("Cookie", "session="+diagnosticCanary)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if strings.Contains(logs.String(), diagnosticCanary) {
		t.Fatal("request or error contents leaked into logs")
	}
	var records []map[string]any
	decoder := json.NewDecoder(&logs)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if record["route"] != "/diagnostics/{id}" || record["request_id"] != response.Header().Get("X-Request-ID") || record["request_id"] == "" {
			t.Errorf("record lacks request identity and route template: %v", record)
		}
		records = append(records, record)
	}
	return response, records
}

func assertInternalResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body api.Error
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || body.Code != "internal_error" || body.Message != "The request could not be completed." || body.RequestId != response.Header().Get("X-Request-ID") || body.Fields != nil {
		t.Fatalf("unexpected internal error response: status %d, body %+v", response.Code, body)
	}
}
