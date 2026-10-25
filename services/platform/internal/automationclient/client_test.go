package automationclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

func TestOriginAndRedirectContainCredentials(t *testing.T) {
	for _, origin := range []string{"https://user:password@example.test", "https://example.test/path", "https://example.test?x=y", "http://example.test", "http://localhost:8080", "http://192.0.2.10"} {
		if _, err := New(origin, "astilba_auto_fixture", true); err == nil {
			t.Fatal("accepted a non-origin or non-loopback development URL")
		}
	}
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c, err := New(origin.URL, "astilba_auto_fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Context(context.Background()); err == nil || reached.Load() {
		t.Fatal("management redirected the authenticated client")
	}
}

func TestResponsesAreBoundedAndMutationsAreNotRetried(t *testing.T) {
	token := "astilba_auto_private_canary"
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing explicit bearer identity")
		}
		if r.Method == "PUT" {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"code":%q,"message":%q}`, token, token)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", responseLimit+1))
	}))
	defer server.Close()
	c, err := New(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Context(context.Background())
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("unbounded response accepted", err)
	}
	_, err = c.UpdateDraft(context.Background(), api.AutomationContext{}, api.DraftUpdate{BaseRevisionId: uuid.New(), Specification: []byte(`{}`)})
	if err == nil || strings.Contains(err.Error(), token) || requests.Load() != 2 {
		t.Fatal("mutation was retried or exposed remote diagnostics", requests.Load())
	}
}
