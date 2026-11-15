package pipelinetest

import (
	"io"
	"net/http"
)

// A fake provider reads what Astilba sends it.
func ServeHTTP(r *http.Request) {
	_, _ = io.ReadAll(r.Body)
}
