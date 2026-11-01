package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func requestRoute(r *http.Request) string {
	if route := chi.RouteContext(r.Context()); route != nil {
		if pattern := route.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

func failureDiagnostic(err error) slog.Attr {
	category := "internal"
	var database *pgconn.PgError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		category = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "context_deadline_exceeded"
	case errors.As(err, &database):
		// PostgreSQL text fields can contain query values; only retain its code.
		if validSQLState(database.Code) {
			return slog.Group("error", "category", "database", "sqlstate", database.Code)
		}
		category = "database"
	case errors.As(err, &network):
		category = "network"
		if network.Timeout() {
			category = "network_timeout"
		}
	}
	return slog.Group("error", "category", category)
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

type panicFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

func panicFrames() []panicFrame {
	var pcs [32]uintptr
	// Skip this helper and the recovery closure, retaining the panic's caller.
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	out := make([]panicFrame, 0)
	for {
		frame, more := frames.Next()
		const module = "github.com/astilbahq/astilba/"
		if strings.HasPrefix(frame.Function, module) {
			out = append(out, panicFrame{Function: strings.TrimPrefix(frame.Function, module), File: filepath.Base(frame.File), Line: frame.Line})
		}
		if !more || len(out) == len(pcs) {
			return out
		}
	}
}
