package other

import (
	"io"
	"net/http"
)

// Only the named fake is allowed, not its neighbours.
func ServeHTTP(r *http.Request) {
	_, _ = io.ReadAll(r.Body) // want `a request body read outside decode`
}
