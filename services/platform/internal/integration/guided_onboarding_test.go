package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/pressly/goose/v3"
)

func TestRecoveryCodeConfirmationIsGenerationBoundAndDoesNotReauthenticate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.browser(t)
	owner.login("owner@example.test", f.password)
	guest := f.browser(t)
	const path = "/api/v1/auth/mfa/recovery-codes/acknowledge"
	guest.call("POST", path, map[string]string{"generation": uuid.NewString()}, 401, nil)
	owner.call("POST", path, map[string]string{"generation": uuid.NewString()}, 403, nil)
	var enrollment api.MFAEnrollment
	owner.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codes api.RecoveryCodes
	owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
	owner.csrf = codes.Session.CsrfToken
	if codes.Generation == nil || codes.Session.User.RecoveryCodesSaved {
		t.Fatal("new codes have no generation or are already marked saved")
	}
	before, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	owner.call("POST", path, map[string]string{"generation": uuid.NewString()}, 409, nil)
	owner.call("POST", path, map[string]string{}, 400, nil)
	future := time.Now().Add(6 * time.Minute)
	f.auth.Now = func() time.Time { return future }
	var saved api.Session
	owner.call("POST", path, map[string]string{"generation": codes.Generation.String()}, 200, &saved)
	owner.csrf = saved.CsrfToken
	if !saved.User.RecoveryCodesSaved || saved.RecentAuth {
		t.Fatal("confirmation did not persist or upgraded recent authentication")
	}
	owner.call("POST", path, map[string]string{"generation": codes.Generation.String()}, 200, &saved)
	after, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil || after.AuthVersion != before.AuthVersion || !bytes.Equal(after.TotpSecret, before.TotpSecret) || after.PasswordHash != before.PasswordHash {
		t.Fatal("saving confirmation changed credentials", err)
	}
	owner.call("POST", "/api/v1/auth/mfa/recovery-codes", nil, 403, nil)
	f.auth.Now = time.Now
	var replacement api.RecoveryCodes
	owner.call("POST", "/api/v1/auth/mfa/recovery-codes", nil, 200, &replacement)
	owner.csrf = replacement.Session.CsrfToken
	if replacement.Generation == nil || *replacement.Generation == *codes.Generation || replacement.Session.User.RecoveryCodesSaved {
		t.Fatal("replacement did not reset confirmation and generation")
	}
	if err := f.auth.AcknowledgeRecoveryCodes(ctx, store.Principal{Account: before, MFA: true}, *codes.Generation); !errors.Is(err, domain.Unauthenticated) {
		t.Fatal("revoked request snapshot acknowledged replacement", err)
	}
	owner.call("POST", path, map[string]string{"generation": codes.Generation.String()}, 409, nil)
	current := owner.session()
	if current.User.RecoveryCodesSaved {
		t.Fatal("old generation marked replacement saved")
	}
	owner.call("POST", path, map[string]string{"generation": replacement.Generation.String()}, 200, &saved)
	owner.call("POST", "/api/v1/auth/mfa/disable", nil, 200, &saved)
	owner.csrf = saved.CsrfToken
	cleared, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil || cleared.RecoveryCodesGeneration != nil || !cleared.RecoveryCodesSaved {
		t.Fatal("disable retained generation or pending confirmation", err)
	}
	owner.call("POST", path, map[string]string{"generation": replacement.Generation.String()}, 403, nil)
}

func TestRecoveryConfirmationMigrationPreservesExistingCredentials(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.browser(t)
	owner.login("owner@example.test", f.password)
	var enrollment api.MFAEnrollment
	owner.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codes api.RecoveryCodes
	owner.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &codes)
	conn, err := sql.Open("pgx", f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, conn, os.DirFS("../../db/migrations"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	var hash string
	var version int64
	var secret []byte
	if err = f.st.Pool.QueryRow(ctx, "SELECT password_hash,auth_version,totp_secret FROM accounts WHERE id=$1", f.owner).Scan(&hash, &version, &secret); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err = dbmigrate.Check(ctx, f.st.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil || !account.RecoveryCodesSaved || account.RecoveryCodesGeneration != nil || account.PasswordHash != hash || account.AuthVersion != version || len(secret) == 0 || !bytes.Equal(account.TotpSecret, secret) {
		t.Fatal("migration changed existing credentials or left legacy setup incomplete", err)
	}
}
