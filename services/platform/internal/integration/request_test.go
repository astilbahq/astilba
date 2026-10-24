package integration

import (
	"bytes"
	"net/http"
)

func newRequest(b *browser, method, path string, data []byte) (*http.Request, error) {
	r, e := http.NewRequest(method, b.f.server.URL+path, bytes.NewReader(data))
	if e == nil {
		r.Header.Set("Origin", b.f.server.URL)
		r.Header.Set("X-CSRF-Token", b.csrf)
		r.Header.Set("Content-Type", "application/json")
	}
	return r, e
}
