package execution

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/riverqueue/river"
)

// Management's provider calls hold the App's cached token, so a 401 from one must drop it, or
// every call until it expires fails the same way (PLAT-401). A stored credential is no cache's.
func TestManagementProviderDropsARejectedAppToken(t *testing.T) {
	var minted atomic.Int64
	var rejected atomic.Value
	rejected.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/installations/2/access_tokens" {
			var in struct {
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &in)
			_ = stablejson.Encode(w, map[string]any{"token": fmt.Sprintf("ghs_%036d", minted.Add(1)), "expires_at": time.Now().Add(time.Hour), "repositories": []map[string]any{{"id": 12}}, "permissions": in.Permissions})
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+rejected.Load().(string) {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/repos/team/app" {
			_ = stablejson.Encode(w, map[string]any{"id": 12, "full_name": "team/app", "private": true})
			return
		}
		_ = stablejson.Encode(w, map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
	}))
	defer server.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := githubapp.New(githubapp.Config{ID: 1, ClientID: "fixture-client", Slug: "fixture", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: server.URL, WebOrigin: server.URL}, "http://127.0.0.1", true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{GitHub: app, Policy: pipeline.Policy{Development: true, GitHubFixture: server.URL}}
	connection := pipeline.Connection{Provider: "github", BaseURL: "https://api.github.com", Repository: "team/app", RepositoryID: 12, Private: true}
	ctx := context.Background()
	resolve := func(token string, minted bool) error {
		t.Helper()
		provider, err := s.newProvider(connection, pipeline.Credential{Username: "x-access-token", Password: token}, minted)
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Resolve(ctx, "main")
		return err
	}
	own, err := app.Mint(ctx, 2, 12, false)
	if err != nil {
		t.Fatal(err)
	}
	rejected.Store(own)
	// A stored credential that happens to match is not the App's to drop.
	if resolve(own, false) == nil {
		t.Fatal("a rejected credential was accepted")
	}
	if again, err := app.Mint(ctx, 2, 12, false); err != nil || again != own {
		t.Fatal("a stored credential's 401 dropped the App's token", err)
	}
	if resolve(own, true) == nil {
		t.Fatal("a rejected credential was accepted")
	}
	next, err := app.Mint(ctx, 2, 12, false)
	if err != nil || next == own || minted.Load() != 2 {
		t.Fatal("a token GitHub rejected stayed cached", err)
	}
	if err = resolve(next, true); err != nil {
		t.Fatal("the new token was not used", err)
	}
}

// GitHub limiting the App, or a cached token it stopped accepting, passes, as does a provider
// limiting a stored credential with a 403, so a submission that meets one is tried again rather
// than failed as if its credential were unusable.
func TestSubmissionRetriesAPassingGitHubFailure(t *testing.T) {
	for _, err := range []error{appFailure(&githubapp.RateLimited{Until: time.Now().Add(time.Minute)}), appFailure(fmt.Errorf("%w: %w", githubapp.ErrTokenRejected, &githubapp.APIError{Status: 401})), &pipeline.ProviderError{Status: 403, Limited: true}} {
		if note, terminal := submissionFailure(err); terminal || !strings.Contains(note, "Try again") {
			t.Fatalf("a passing GitHub failure ended the submission: %q", note)
		}
	}
	if _, terminal := submissionFailure(domain.Invalid("credential")); !terminal {
		t.Fatal("an unusable credential was retried")
	}
	if _, terminal := submissionFailure(&pipeline.ProviderError{Status: 403}); !terminal {
		t.Fatal("a provider's refusal was retried")
	}
}

// A submission that meets GitHub's rate limit waits for GitHub's time if that comes within the
// hour it has, and a snooze never settles it, even on its last attempt.
func TestASubmissionWaitsForGitHubsLimit(t *testing.T) {
	until := time.Now().Add(20 * time.Minute)
	if wait, ok := submissionWait(until, time.Now()); !ok || wait < 19*time.Minute || wait > 20*time.Minute {
		t.Fatal("a submission did not wait for GitHub's time", wait, ok)
	}
	if _, ok := submissionWait(until, time.Now().Add(-50*time.Minute)); ok {
		t.Fatal("a submission waited for a time past its hour")
	}
	if wait, ok := submissionWait(time.Now().Add(-time.Second), time.Now()); !ok || wait != time.Second {
		t.Fatal("a passed time did not wait briefly", wait)
	}
	if settlesSubmission(river.JobSnooze(time.Minute), submissionAttempts) {
		t.Fatal("a snoozed last attempt settled its submission")
	}
	if !settlesSubmission(errGitHubBusy, submissionAttempts) || settlesSubmission(errGitHubBusy, submissionAttempts-1) {
		t.Fatal("a failed attempt settled at the wrong time")
	}
}
