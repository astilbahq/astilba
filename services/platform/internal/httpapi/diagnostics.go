package httpapi

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"
)

func requestRoute(r *http.Request) string {
	if route := chi.RouteContext(r.Context()); route != nil {
		if pattern := route.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
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
