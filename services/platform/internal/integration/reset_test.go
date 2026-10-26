package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
)

func TestResetLinkValidationDoesNotConsumeOrChangeCredentials(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.browser(t)
	owner.login("owner@example.test", f.password)
	guest := f.browser(t)
	before, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	requestLink := func() string {
		t.Helper()
		guest.call("POST", "/api/v1/auth/reset/request", map[string]string{"email": "owner@example.test"}, 202, nil)
		return tokenFromLinkMail(t, f, "/reset")
	}
	check := func(token string, status int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"token": token})
		request, err := newRequest(guest, "POST", "/api/v1/auth/reset/validate", body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := guest.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != status || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("validation status=%d cache=%q", response.StatusCode, response.Header.Get("Cache-Control"))
		}
		if status == http.StatusNoContent {
			if len(data) != 0 {
				t.Fatal("validity check returned metadata")
			}
		} else {
			var failure api.Error
			if err := json.Unmarshal(data, &failure); err != nil || failure.Code != "invalid_credentials" {
				t.Fatal("validation did not return a generic credential error", err)
			}
			if strings.Contains(string(data), token) || strings.Contains(string(data), before.Email) || strings.Contains(string(data), before.ID.String()) {
				t.Fatal("validation leaked reset or account metadata")
			}
		}
	}
	token := requestLink()
	reset, err := f.st.Queries.PeekPasswordReset(ctx, auth.Hash(token))
	if err != nil {
		t.Fatal(err)
	}
	check("unknown-auth-link-token", http.StatusBadRequest)
	check(token, http.StatusNoContent)
	check(token, http.StatusNoContent)
	after, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil || after.AuthVersion != before.AuthVersion || after.PasswordHash != before.PasswordHash {
		t.Fatal("validity check changed account credentials", err)
	}
	stillValid, err := f.st.Queries.PeekPasswordReset(ctx, auth.Hash(token))
	if err != nil || !stillValid.ExpiresAt.Equal(reset.ExpiresAt) {
		t.Fatal("validity check consumed or extended the reset link", err)
	}
	if owner.session().User == nil || guest.session().User != nil {
		t.Fatal("validity check changed authentication state")
	}

	replacement := requestLink()
	check(token, http.StatusBadRequest)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE password_resets SET expires_at=now()-interval '1 second' WHERE account_id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	check(replacement, http.StatusBadRequest)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE password_resets SET expires_at=now()+interval '30 minutes' WHERE account_id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	check(replacement, http.StatusNoContent)
	guest.call("POST", "/api/v1/auth/reset/complete", map[string]string{"token": replacement, "password": "replacement password after validation"}, 204, nil)
	guest = f.browser(t)
	check(replacement, http.StatusBadRequest)
}

func TestAuthLinkValidationProtectionAndRateLimit(t *testing.T) {
	for _, test := range []struct{ name, path, complete string }{
		{"reset", "/api/v1/auth/reset/validate", "/api/v1/auth/reset/complete"},
		{"invitation", "/api/v1/auth/invitations/validate", "/api/v1/auth/invitations/accept"},
		{"signup", "/api/v1/hosting/signup/validate", "/api/v1/hosting/signup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			if test.name == "signup" {
				f.st.Hosted = true
			}
			guest := f.browser(t)
			for _, change := range []func(*http.Request){
				func(r *http.Request) { r.Header.Del("X-CSRF-Token") },
				func(r *http.Request) { r.Header.Set("Origin", "https://untrusted.example") },
			} {
				request, err := newRequest(guest, "POST", test.path, []byte(`{"token":"unknown-auth-link-token"}`))
				if err != nil {
					t.Fatal(err)
				}
				change(request)
				response, err := guest.client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusForbidden {
					t.Fatal("unprotected link validation accepted", response.StatusCode)
				}
				if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
					t.Fatal("link validity responses can be cached")
				}
			}
			for _, body := range []map[string]string{{"token": ""}, {"token": strings.Repeat("a", 129)}, {"token": "unknown", "email": "owner@example.test"}} {
				var failure api.Error
				guest.call("POST", test.path, body, 400, &failure)
				if failure.Code != "invalid_input" {
					t.Fatal("invalid link validation request reached the service")
				}
			}
			for range 60 {
				guest.call("POST", test.path, map[string]string{"token": "unknown-auth-link-token"}, 400, nil)
			}
			var limited api.Error
			guest.call("POST", test.path, map[string]string{"token": "unknown-auth-link-token"}, 429, &limited)
			if limited.Code != "rate_limited" {
				t.Fatal("link validity checks are not rate limited")
			}
			// Checking a link cannot exhaust the separate submission budget.
			var rejected api.Error
			body := map[string]string{"token": "unknown-auth-link-token", "password": "a valid replacement password"}
			if test.name == "signup" {
				body["workspace"] = "Workspace"
				body["name"] = "Owner"
			}
			guest.call("POST", test.complete, body, 400, &rejected)
			if rejected.Code != "invalid_credentials" {
				t.Fatal("submission did not reach its independent token check")
			}

		})
	}
}
