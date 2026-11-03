package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func TestHostedLinkValidationDoesNotClaimAdmission(t *testing.T) {
	f := setupBootstrap(t, false)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 1, ServerLimit: 2}
	ctx := context.Background()
	if err := f.auth.InviteHosted(ctx, "beta@example.test", f.apiServer.Origin); err != nil {
		t.Fatal(err)
	}
	token := tokenFromLinkMail(t, f, "/signup")
	guest := f.browser(t)
	for range 2 {
		guest.call("POST", "/api/v1/hosting/signup/validate", map[string]string{"token": token}, 204, nil)
	}
	var claimed *time.Time
	var accounts, workspaces int
	if err := f.st.Pool.QueryRow(ctx, `SELECT claimed_at,(SELECT count(*) FROM accounts),(SELECT count(*) FROM workspaces) FROM hosted_invitations WHERE token_hash=$1`, auth.Hash(token)).Scan(&claimed, &accounts, &workspaces); err != nil {
		t.Fatal(err)
	}
	if claimed != nil || accounts != 0 || workspaces != 0 || guest.session().User != nil {
		t.Fatal("checking invitation changed admission or authentication")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE hosted_invitations SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, auth.Hash(token)); err != nil {
		t.Fatal(err)
	}
	var failure api.Error
	guest.call("POST", "/api/v1/hosting/signup/validate", map[string]string{"token": token}, 400, &failure)
	if failure.Code != "invalid_credentials" {
		t.Fatal("expiry leaked admission metadata")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE hosted_invitations SET expires_at=now()+interval '1 day' WHERE token_hash=$1`, auth.Hash(token)); err != nil {
		t.Fatal(err)
	}
	var session api.Session
	guest.call("POST", "/api/v1/hosting/signup", map[string]string{"token": token, "workspace": "Beta", "name": "Owner", "password": f.password}, 200, &session)
	guest.csrf = session.CsrfToken
	guest.call("POST", "/api/v1/hosting/signup/validate", map[string]string{"token": token}, 400, &failure)
	if failure.Code != "invalid_credentials" || session.User == nil || !session.User.EmailVerified || len(session.Workspaces) != 1 {
		t.Fatal("single-use admission changed")
	}
	f.st.Hosted = false
	guest.call("POST", "/api/v1/hosting/signup/validate", map[string]string{"token": token}, 403, nil)
}

func TestEmailVerificationLinksAreAccountBoundAndExplicit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.browser(t)
	before := owner.login("owner@example.test", f.password)
	guest := f.browser(t)
	for _, path := range []string{"validate", "verify"} {
		guest.call("POST", "/api/v1/auth/email/"+path, map[string]string{"token": "unknown-verification-token"}, 401, nil)
	}
	owner.call("POST", "/api/v1/auth/email/request", nil, 204, nil)
	token := tokenFromLinkMail(t, f, "/verify-email")
	var expiry time.Time
	if err := f.st.Pool.QueryRow(ctx, "SELECT expires_at FROM email_verifications WHERE account_id=$1", f.owner).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	otherAccount, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: "other@example.test", Name: "Other", PasswordHash: mustAccountHash(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	other := f.browser(t)
	other.login(otherAccount.Email, f.password)
	for _, path := range []string{"validate", "verify"} {
		var failure api.Error
		data := other.call("POST", "/api/v1/auth/email/"+path, map[string]string{"token": token}, 400, &failure)
		if failure.Code != "invalid_credentials" || strings.Contains(string(data), token) || strings.Contains(string(data), "owner@example.test") {
			t.Fatal("cross-account capability leaked metadata")
		}
	}
	for range 2 {
		owner.call("POST", "/api/v1/auth/email/validate", map[string]string{"token": token}, 204, nil)
	}
	var stillExpiry time.Time
	if err := f.st.Pool.QueryRow(ctx, "SELECT expires_at FROM email_verifications WHERE account_id=$1", f.owner).Scan(&stillExpiry); err != nil {
		t.Fatal(err)
	}
	after := owner.session()
	if after.User.EmailVerified || before.RecentAuth != after.RecentAuth || !expiry.Equal(stillExpiry) {
		t.Fatal("checking email link verified, extended or refreshed authentication")
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE email_verifications SET expires_at=now()-interval '1 second' WHERE account_id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	owner.call("POST", "/api/v1/auth/email/validate", map[string]string{"token": token}, 400, nil)
	owner.call("POST", "/api/v1/auth/email/request", nil, 204, nil)
	replacement := tokenFromLinkMail(t, f, "/verify-email")
	owner.call("POST", "/api/v1/auth/email/validate", map[string]string{"token": token}, 400, nil)
	owner.call("POST", "/api/v1/auth/email/validate", map[string]string{"token": replacement}, 204, nil)
	var verified api.Session
	owner.call("POST", "/api/v1/auth/email/verify", map[string]string{"token": replacement}, 200, &verified)
	if verified.User == nil || !verified.User.EmailVerified || verified.RecentAuth != before.RecentAuth {
		t.Fatal("explicit verification changed identity freshness")
	}
	owner.csrf = verified.CsrfToken
	owner.call("POST", "/api/v1/auth/email/validate", map[string]string{"token": replacement}, 400, nil)
	owner.call("POST", "/api/v1/auth/email/verify", map[string]string{"token": replacement}, 400, nil)
}

func TestUndeliveredLinksReleaseTheirTokens(t *testing.T) {
	f := setupBootstrap(t, false)
	f.st.Hosted = true
	f.st.Hosting = hosting.Config{CohortLimit: 1, ServerLimit: 2}
	ctx := context.Background()
	unavailable, err := mailer.New(mailer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	failing := *f.auth
	failing.Mail = unavailable
	if err = failing.InviteHosted(ctx, "beta@example.test", f.apiServer.Origin); !errors.Is(err, mailer.ErrUnavailable) {
		t.Fatalf("undelivered invitation returned %v", err)
	}
	var invitations int
	if err = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM hosted_invitations").Scan(&invitations); err != nil {
		t.Fatal(err)
	}
	if invitations != 0 {
		t.Fatal("undelivered invitation kept its cohort slot")
	}
	if err = f.auth.InviteHosted(ctx, "beta@example.test", f.apiServer.Origin); err != nil {
		t.Fatalf("released cohort slot was not reusable: %v", err)
	}
	tokenFromLinkMail(t, f, "/signup")

	account, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: "unverified@example.test", Name: "Unverified", PasswordHash: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err = failing.RequestEmailVerification(ctx, store.Principal{Account: account}, f.apiServer.Origin); !errors.Is(err, mailer.ErrUnavailable) {
		t.Fatalf("undelivered verification returned %v", err)
	}
	var verifications int
	if err = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM email_verifications WHERE account_id=$1", account.ID).Scan(&verifications); err != nil {
		t.Fatal(err)
	}
	if verifications != 0 {
		t.Fatal("undelivered email verification remained redeemable")
	}
}

func mustAccountHash(t *testing.T, f *fixture) string {
	t.Helper()
	a, e := f.st.Queries.AccountByID(context.Background(), f.owner)
	if e != nil {
		t.Fatal(e)
	}
	return a.PasswordHash
}

func TestEmailLinkValidationProtectionAndBudget(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	b.login("owner@example.test", f.password)
	const path = "/api/v1/auth/email/validate"
	request, err := newRequest(b, "POST", path, []byte(`{"token":"unknown-verification-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Del("X-CSRF-Token")
	response, err := b.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("email check lost CSRF/cache protections")
	}
	for range 60 {
		b.call("POST", path, map[string]string{"token": "unknown-verification-token"}, 400, nil)
	}
	var limited api.Error
	b.call("POST", path, map[string]string{"token": "unknown-verification-token"}, 429, &limited)
	if limited.Code != "rate_limited" {
		t.Fatal("email checks unbounded")
	}
	var rejected api.Error
	b.call("POST", "/api/v1/auth/email/verify", map[string]string{"token": "unknown-verification-token"}, 400, &rejected)
	if rejected.Code != "invalid_credentials" {
		t.Fatal("validation exhausted independent verification budget")
	}

}
