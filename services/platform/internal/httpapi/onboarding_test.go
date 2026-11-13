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

// Management serves only its release key, re-encoded. A configured file holding anything more,
// such as the private key appended by mistake, is refused rather than partly served.
func TestPublicReleaseKeyIsServedCanonically(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(public)
	canonical := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	privateDER, _ := x509.MarshalPKCS8PrivateKey(private)
	withPrivate := append(bytes.Clone(canonical), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})...)
	if _, e := agentrelease.PublicKey(withPrivate); e == nil {
		t.Fatal("a release key file holding a private key too was accepted")
	}
	key, e := agentrelease.PublicKey(append(append([]byte("\n"), canonical...), "\r\n"...))
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
		t.Fatalf("public endpoint served %q with status %d, not the key alone", response.Body.Bytes(), response.Code)
	}
}
