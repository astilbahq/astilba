package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

func codeFailureCount(t *testing.T, f *fixture) (count int) {
	t.Helper()
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT attempts FROM auth_attempts WHERE scope_hash=$1", auth.Hash(fmt.Sprintf("code-failures:%s", f.owner))).Scan(&count)
	return count
}

// forgetBudget ends one short-window budget early, so a test can reach the daily one.
func forgetBudget(t *testing.T, f *fixture, prefix string) {
	t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), "DELETE FROM auth_attempts WHERE scope_hash=$1", auth.Hash(prefix+f.owner.String())); err != nil {
		t.Fatal(err)
	}
}

// signInWithCode signs the owner in with the password from a new browser, then gives code as
// the second factor. Each sign-in restarts the login budgets, as it would for a guesser who
// knows the password.
func signInWithCode(t *testing.T, f *fixture, code string) (int, []byte) {
	t.Helper()
	other := f.browser(t)
	other.login("owner@example.test", f.password)
	forgetBudget(t, f, "mfa:")
	return other.request("POST", "/api/v1/auth/mfa", map[string]string{"code": code})
}

// refusal is an error response without its request ID, which differs on every response.
func refusal(t *testing.T, body []byte) api.Error {
	t.Helper()
	var e api.Error
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	e.RequestId = ""
	return e
}

// Second factors at sign-in and when confirming identity spend one daily allowance of failures,
// which signing in again with the password does not restart. A success gets its charge back,
// a spent allowance refuses a right code exactly as a wrong one, and a recovery code still
// signs the owner in.
func TestSecondFactorFailuresShareADailyAllowance(t *testing.T) {
	f := setup(t)
	b, _, secret, codes := staleOwner(t, f, true)
	now := f.auth.Now()
	for range 10 {
		if status, body := signInWithCode(t, f, "000000"); status != 400 {
			t.Fatalf("a wrong code at sign-in: status %d: %s", status, body)
		}
	}
	for i := range 9 {
		if i%5 == 0 {
			forgetBudget(t, f, "reauth:")
		}
		b.reauthenticate(map[string]string{"code": "000000"}, 400)
	}
	if got := codeFailureCount(t, f); got != 19 {
		t.Fatalf("%d failures counted, want 19", got)
	}
	if status, body := signInWithCode(t, f, codeAt(t, secret, now)); status != 200 {
		t.Fatalf("a right code at sign-in: status %d: %s", status, body)
	}
	if got := codeFailureCount(t, f); got != 19 {
		t.Fatalf("a success left %d failures counted, want 19", got)
	}
	if status, body := signInWithCode(t, f, "000000"); status != 400 {
		t.Fatalf("the twentieth failure: status %d: %s", status, body)
	}
	if got := codeFailureCount(t, f); got != 20 {
		t.Fatalf("%d failures counted, want 20", got)
	}
	status, wrong := signInWithCode(t, f, "000001")
	if status != 429 {
		t.Fatalf("the twenty-first failure: status %d: %s", status, wrong)
	}
	status, right := signInWithCode(t, f, codeAt(t, secret, now.Add(30*time.Second)))
	if status != 429 || refusal(t, wrong) != refusal(t, right) {
		t.Fatalf("a spent allowance answered a right code differently: status %d: %s, after %s", status, right, wrong)
	}
	forgetBudget(t, f, "reauth:")
	b.reauthenticate(map[string]string{"code": codeAt(t, secret, now.Add(30*time.Second))}, 429)
	if got := codeFailureCount(t, f); got != 20 {
		t.Fatalf("refusals were counted: %d failures, want 20", got)
	}
	// Recovery codes still work, both when confirming identity and at sign-in.
	forgetBudget(t, f, "reauth:")
	b.reauthenticate(map[string]string{"code": codes.Codes[1]}, 200)
	if status, body := signInWithCode(t, f, codes.Codes[0]); status != 200 {
		t.Fatalf("a recovery code with the allowance spent: status %d: %s", status, body)
	}
}

// Concurrent guesses at sign-in cannot overrun the daily allowance between them: each is
// charged a failure before its code is checked.
func TestSignInConcurrentGuessesStayWithinTheDailyAllowance(t *testing.T) {
	f := setup(t)
	staleOwner(t, f, true)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,19,now()+interval '1 day')", auth.Hash(fmt.Sprintf("code-failures:%s", f.owner))); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := f.auth.VerifyCode(ctx, f.owner, account.AuthVersion, "000000", nil)
			results <- err
		}()
	}
	checked := 0
	for range 5 {
		switch err := <-results; {
		case errors.Is(err, domain.Credentials):
			checked++
		case !errors.Is(err, domain.RateLimited):
			t.Fatal("unexpected refusal", err)
		}
	}
	if checked != 1 {
		t.Fatalf("%d guesses were checked with one failure left in the allowance", checked)
	}
}

// A pending sign-in that a credential change has ended checks no code, so refusing it is not a
// failure.
func TestEndedPendingSignInSpendsNoFailure(t *testing.T) {
	f := setup(t)
	_, _, secret, _ := staleOwner(t, f, true)
	ctx := context.Background()
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.VerifyCode(ctx, f.owner, account.AuthVersion-1, codeAt(t, secret, f.auth.Now()), nil); !errors.Is(err, domain.Credentials) {
		t.Fatal("an ended pending sign-in was accepted", err)
	}
	if got := codeFailureCount(t, f); got != 0 {
		t.Fatalf("an ended pending sign-in spent %d failures", got)
	}
}

// Activating an authenticator restarts the code's allowance, and failed codes never refuse the
// password, so an owner whose codes were spent can turn two-factor authentication off with a
// recovery code, enrol again and sign in with the new authenticator at once.
func TestEnrollingAgainRestartsTheCodeAllowance(t *testing.T) {
	f := setup(t)
	b, _, _, codes := staleOwner(t, f, true)
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO auth_attempts(scope_hash,attempts,expires_at) VALUES($1,20,now()+interval '1 day')", auth.Hash(fmt.Sprintf("code-failures:%s", f.owner))); err != nil {
		t.Fatal(err)
	}
	var session api.Session
	b.call("POST", "/api/v1/auth/mfa/disable", map[string]any{"current": map[string]string{"code": codes.Codes[0]}}, 200, &session)
	b.csrf = session.CsrfToken
	secret := enrollSecret(t, b)
	var enrolled api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": codeAt(t, secret, f.auth.Now()), "current": map[string]string{"password": f.password}}, 200, &enrolled)
	if got := codeFailureCount(t, f); got != 0 {
		t.Fatalf("enrolling left %d code failures counted", got)
	}
	if status, body := signInWithCode(t, f, codeAt(t, secret, f.auth.Now().Add(30*time.Second))); status != 200 {
		t.Fatalf("the new authenticator at sign-in: status %d: %s", status, body)
	}
}

// Without two-factor authentication, wrong passwords when confirming identity spend their own
// daily allowance, and a right one gets its charge back.
func TestPasswordConfirmationFailuresSpendADailyAllowance(t *testing.T) {
	f := setup(t)
	b, _, _, _ := staleOwner(t, f, false)
	failures := func() (count int) {
		t.Helper()
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT attempts FROM auth_attempts WHERE scope_hash=$1", auth.Hash(fmt.Sprintf("reauth-failures:%s", f.owner))).Scan(&count)
		return count
	}
	b.reauthenticate(map[string]string{"password": f.password}, 200)
	if got := failures(); got != 0 {
		t.Fatalf("a right password left %d failures counted", got)
	}
	for range 4 {
		forgetBudget(t, f, "reauth:")
		for range 5 {
			b.reauthenticate(map[string]string{"password": "not the password at all"}, 400)
		}
	}
	if got := failures(); got != 20 {
		t.Fatalf("%d failures counted, want 20", got)
	}
	forgetBudget(t, f, "reauth:")
	b.reauthenticate(map[string]string{"password": f.password}, 429)
}
