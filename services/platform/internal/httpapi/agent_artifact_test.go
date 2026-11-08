package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/go-chi/chi/v5"
)

// The artifact route serves only files the release catalog verified, found by the release's own
// digest. Any other name, including one that reaches elsewhere in or beside the catalog, is absent.
func TestAgentArtifactServesOnlyLoadedReleases(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "releases")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("verified agent artifact")
	sum := sha256.Sum256(artifact)
	digest := hex.EncodeToString(sum[:])
	for name, body := range map[string][]byte{
		filepath.Join(dir, digest):         artifact,
		filepath.Join(dir, digest+".json"): []byte("{}"),
		filepath.Join(dir, "unlisted"):     []byte("not a release"),
		filepath.Join(root, "outside"):     []byte("outside the catalog"),
	} {
		if err := os.WriteFile(name, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Execution: &execution.Service{Releases: []api.AgentRelease{{Sha256: digest}}}, ArtifactDirectory: dir}
	router := chi.NewRouter()
	router.Get("/agent-artifacts/{sha}", s.agentArtifact)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agent-artifacts/"+digest, nil))
	if response.Code != http.StatusOK || response.Body.String() != string(artifact) || response.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("release: %d %q %q", response.Code, response.Body.String(), response.Header().Get("Content-Type"))
	}

	for _, path := range []string{
		"/agent-artifacts/unlisted",
		"/agent-artifacts/" + digest + ".json",
		"/agent-artifacts/..%2Foutside",
		"/agent-artifacts/%2E%2E%2Foutside",
		"/agent-artifacts/" + digest[:63],
		"/agent-artifacts/" + digest + "%2F..%2F..%2Foutside",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: %d %q", path, response.Code, response.Body.String())
		}
	}

	s.ArtifactDirectory = ""
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agent-artifacts/"+digest, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("without a catalog directory: %d", response.Code)
	}
}
