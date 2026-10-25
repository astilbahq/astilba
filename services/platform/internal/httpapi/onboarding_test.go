package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
)

func TestPublicReleaseKeyExcludesConfiguredFileRemainder(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(public)
	canonical := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	privateDER, _ := x509.MarshalPKCS8PrivateKey(private)
	configured := append(bytes.Clone(canonical), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})...)
	configured = append(configured, []byte("private operator annotation")...)
	key, e := agentrelease.PublicKey(configured)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Execution: &execution.Service{}, AgentReleaseKey: key, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler, e := s.Handler(http.NotFoundHandler())
	if e != nil {
		t.Fatal(e)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/agent-public-key.pem", nil))
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), canonical) {
		t.Fatalf("public endpoint exposed configured-file remainder: status %d", response.Code)
	}
}
