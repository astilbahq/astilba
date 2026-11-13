package httpapi

import (
	"encoding/json/v2"
	"io"
	"net/http"
)

type Server struct{}

func decode[T any](r *http.Request) (T, error) {
	var v T
	err := json.UnmarshalRead(r.Body, &v)
	return v, err
}

func (s *Server) limit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
}

func (s *Server) ReceiveGitPush(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
}

func (s *Server) Rename(w http.ResponseWriter, r *http.Request) {
	var v struct{ Name string }
	_ = json.UnmarshalRead(r.Body, &v) // want `a request body read outside decode`
	_, _ = decode[struct{ Name string }](r)
}

func (s *Server) swap(w http.ResponseWriter, r *http.Request, other io.ReadCloser) {
	r.Body = other                                              // want `a request body read outside decode`
	r.Body = http.MaxBytesReader(w, other, 1024)                // want `a request body read outside decode`
	r.Body = http.MaxBytesReader(w, io.NopCloser(r.Body), 1024) // want `a request body read outside decode` `a request body read outside decode`
}

func (s *Server) responses(res *http.Response) {
	_, _ = io.ReadAll(res.Body)
}
