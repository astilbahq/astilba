package other

import (
	"io"
	"net/http"
)

// decode in another package is not the boundary.
func decode(r *http.Request) {
	_, _ = io.ReadAll(r.Body) // want `a request body read outside decode`
}

func ReceiveGitPush(r *http.Request) {
	_, _ = io.ReadAll(r.Body) // want `a request body read outside decode`
}
