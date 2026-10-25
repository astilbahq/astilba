package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

type Service struct {
	Store     *store.Store
	Passwords *Passwords
	Mail      *mailer.Mailer
	Now       func() time.Time
}

func Hash(value string) []byte { hash := sha256.Sum256([]byte(value)); return hash[:] }

func (s *Service) Limit(ctx context.Context, scope string, max int32, window time.Duration) error {
	if err := s.Store.Queries.PruneAuthAttempts(ctx); err != nil {
		return err
	}
	count, err := s.Store.Queries.RecordAuthAttempt(ctx, db.RecordAuthAttemptParams{ScopeHash: Hash(scope), ExpiresAt: s.Now().Add(window)})
	if err != nil {
		return err
	}
	if count > max {
		return domain.RateLimited
	}
	return nil
}
func (s *Service) Login(ctx context.Context, email, password, ip string) (db.Account, error) {
	var zero db.Account
	if err := s.Limit(ctx, "password:global", 1000, 15*time.Minute); err != nil {
		return zero, err
	}
	if err := s.Limit(ctx, "password:ip:"+ip, 50, 15*time.Minute); err != nil {
		return zero, err
	}
	email, err := domain.Email(email)
	if err != nil {
		return zero, domain.Credentials
	}
	if err := s.Limit(ctx, "password:account:"+email, 5, 15*time.Minute); err != nil {
		return zero, err
	}
	account, err := s.Store.Queries.AccountByEmail(ctx, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	hash := account.PasswordHash
	if err != nil {
		hash = s.Passwords.Dummy
	}
	if err := s.Passwords.Compare(ctx, password, hash); err != nil {
		return zero, err
	}
	if account.ID == uuid.Nil {
		return zero, domain.Credentials
	}
	if err := s.Store.Queries.ClearAuthAttempts(ctx, Hash("password:account:"+email)); err != nil {
		return zero, err
	}
	return account, nil
}

func matchTOTP(code, secret string, now time.Time, last int64) (int64, bool) {
	step := now.Unix() / 30
	for _, candidate := range []int64{step, step - 1, step + 1} {
		if candidate <= last || candidate < 0 {
			continue
		}
		ok, err := totp.ValidateCustom(code, secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && ok {
			return candidate, true
		}
	}
	return 0, false
}
func (s *Service) checkCode(ctx context.Context, q *db.Queries, account db.Account, code string) error {
	code = strings.TrimSpace(code)
	if len(account.TotpSecret) == 0 {
		return domain.Credentials
	}
	secret, err := s.Store.Keys.Open(account.TotpSecret, store.TOTPIdentity(account.ID, false))
	if err != nil {
		return err
	}
	if step, ok := matchTOTP(code, string(secret), s.Now(), account.TotpLastStep); ok {
		return q.AcceptTOTPStep(ctx, db.AcceptTOTPStepParams{ID: account.ID, TotpLastStep: step})
	}
	_, err = q.ConsumeRecoveryCode(ctx, db.ConsumeRecoveryCodeParams{AccountID: account.ID, CodeHash: Hash(strings.ToUpper(code))})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credentials
	}
	return err
}
func (s *Service) VerifyCode(ctx context.Context, id uuid.UUID, expectedVersion int64, code string) (db.Account, error) {
	var account db.Account
	if err := s.Limit(ctx, "mfa:"+id.String(), 5, 5*time.Minute); err != nil {
		return account, err
	}
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = q.LockAccount(ctx, id)
		if err != nil {
			return err
		}
		if account.AuthVersion != expectedVersion {
			return domain.Credentials
		}
		return s.checkCode(ctx, q, account, code)
	})
	return account, err
}
func (s *Service) Reauthenticate(ctx context.Context, id uuid.UUID, password, code string) (db.Account, error) {
	var account db.Account
	if err := s.Limit(ctx, "reauth:"+id.String(), 5, 15*time.Minute); err != nil {
		return account, err
	}
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = q.LockAccount(ctx, id)
		if err != nil {
			return err
		}
		if err = s.Passwords.Compare(ctx, password, account.PasswordHash); err != nil {
			return err
		}
		if len(account.TotpSecret) > 0 {
			return s.checkCode(ctx, q, account, code)
		}
		return nil
	})
	return account, err
}
func recoveryCodes(ctx context.Context, q *db.Queries, id uuid.UUID) ([]string, error) {
	if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
		return nil, err
	}
	codes := make([]string, 10)
	for i := range codes {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		codes[i] = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random[:])
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{AccountID: id, CodeHash: Hash(codes[i])}); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// lockCurrent fences requests authorized before a concurrent credential change.
func lockCurrent(ctx context.Context, q *db.Queries, id uuid.UUID, version int64) (db.Account, error) {
	account, err := q.LockAccount(ctx, id)
	if err != nil {
		return account, err
	}
	if account.AuthVersion != version {
		return account, domain.Unauthenticated
	}
	return account, nil
}

func (s *Service) BeginMFA(ctx context.Context, expected db.Account) (string, string, error) {
	var secret, uri string
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		account, err := lockCurrent(ctx, q, expected.ID, expected.AuthVersion)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) > 0 {
			return domain.Conflict
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "Astilba", AccountName: account.Email, Period: 30, SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return err
		}
		blob, err := s.Store.Keys.Seal([]byte(key.Secret()), store.TOTPIdentity(account.ID, true))
		if err != nil {
			return err
		}
		expires := s.Now().Add(10 * time.Minute)
		if err = q.SetPendingTOTP(ctx, db.SetPendingTOTPParams{ID: account.ID, TotpPending: blob, TotpPendingExpiry: &expires}); err != nil {
			return err
		}
		secret, uri = key.Secret(), key.URL()
		return nil
	})
	return secret, uri, err
}
func (s *Service) ConfirmMFA(ctx context.Context, id uuid.UUID, version int64, code string) (db.Account, []string, error) {
	var account db.Account
	var codes []string
	if err := s.Limit(ctx, "mfa-confirm:"+id.String(), 5, 5*time.Minute); err != nil {
		return account, nil, err
	}
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = lockCurrent(ctx, q, id, version)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) > 0 || account.TotpPendingExpiry == nil || !account.TotpPendingExpiry.After(s.Now()) {
			return domain.Credentials
		}
		secret, err := s.Store.Keys.Open(account.TotpPending, store.TOTPIdentity(id, true))
		if err != nil {
			return err
		}
		step, ok := matchTOTP(strings.TrimSpace(code), string(secret), s.Now(), -1)
		if !ok {
			return domain.Credentials
		}
		active, err := s.Store.Keys.Seal(secret, store.TOTPIdentity(id, false))
		if err != nil {
			return err
		}
		if _, err = q.ActivateTOTP(ctx, db.ActivateTOTPParams{ID: id, TotpLastStep: step, TotpSecret: active}); err != nil {
			return err
		}
		codes, err = recoveryCodes(ctx, q, id)
		if err != nil {
			return err
		}
		account, err = q.AccountByID(ctx, id)
		return err
	})
	return account, codes, err
}
func (s *Service) DisableMFA(ctx context.Context, id uuid.UUID, version int64) (db.Account, error) {
	var account db.Account
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := lockCurrent(ctx, q, id, version); err != nil {
			return err
		}
		if _, err := q.DisableTOTP(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
			return err
		}
		var err error
		account, err = q.AccountByID(ctx, id)
		return err
	})
	return account, err
}
func (s *Service) RegenerateRecovery(ctx context.Context, id uuid.UUID, version int64) (db.Account, []string, error) {
	var account db.Account
	var codes []string
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = lockCurrent(ctx, q, id, version)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) == 0 {
			return domain.Conflict
		}
		codes, err = recoveryCodes(ctx, q, id)
		if err != nil {
			return err
		}
		if err := q.BumpAuthVersion(ctx, id); err != nil {
			return err
		}
		account, err = q.AccountByID(ctx, id)
		return err
	})
	return account, codes, err
}
func (s *Service) ChangePassword(ctx context.Context, id uuid.UUID, version int64, password string) (db.Account, error) {
	var account db.Account
	hash, err := s.Passwords.Hash(ctx, password)
	if err != nil {
		return account, err
	}
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := lockCurrent(ctx, q, id, version); err != nil {
			return err
		}
		if err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: id, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.DeletePasswordReset(ctx, id); err != nil {
			return err
		}
		var err error
		account, err = q.AccountByID(ctx, id)
		return err
	})
	return account, err
}
func (s *Service) RequestReset(ctx context.Context, email, ip, origin string) error {
	if err := s.Limit(ctx, "reset:ip:"+ip, 20, 15*time.Minute); err != nil {
		return err
	}
	email, err := domain.Email(email)
	if err != nil {
		return nil
	}
	if err := s.Limit(ctx, "reset:email:"+email, 3, 15*time.Minute); err != nil {
		return err
	}
	account, err := s.Store.Queries.AccountByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := cryptobox.Token()
	if err != nil {
		return err
	}
	if err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := q.LockAccount(ctx, account.ID); err != nil {
			return err
		}
		return q.InsertPasswordReset(ctx, db.InsertPasswordResetParams{AccountID: account.ID, TokenHash: Hash(token), ExpiresAt: s.Now().Add(30 * time.Minute)})
	}); err != nil {
		return err
	}
	if err := s.Mail.Send(ctx, email, "Reset your Astilba password", "Open this one-use link to reset your password within 30 minutes:\n\n"+origin+"/reset?"+url.Values{"token": {token}}.Encode()); err != nil {
		_ = s.Store.Queries.DeleteUnsentReset(ctx, db.DeleteUnsentResetParams{AccountID: account.ID, TokenHash: Hash(token)})
		return err
	}
	return nil
}
func (s *Service) CompleteReset(ctx context.Context, token, password, ip string) error {
	if err := s.Limit(ctx, "reset-complete:ip:"+ip, 20, 15*time.Minute); err != nil {
		return err
	}
	hash, err := s.Passwords.Hash(ctx, password)
	if err != nil {
		return err
	}
	peek, err := s.Store.Queries.PeekPasswordReset(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credentials
	}
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := q.LockAccount(ctx, peek.AccountID); err != nil {
			return err
		}
		reset, err := q.LockPasswordReset(ctx, Hash(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Credentials
		}
		if err != nil {
			return err
		}
		if reset.AccountID != peek.AccountID {
			return domain.Credentials
		}
		if err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: reset.AccountID, PasswordHash: hash}); err != nil {
			return err
		}
		return q.DeletePasswordReset(ctx, reset.AccountID)
	})
}

func (s *Service) AcceptInvitation(ctx context.Context, token, name, password string, current *db.Account) (db.Account, error) {
	var account db.Account
	peek, err := s.Store.Queries.PeekInvitation(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return account, domain.Credentials
	}
	if err != nil {
		return account, err
	}
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := q.LockWorkspaceByID(ctx, peek.WorkspaceID); err != nil {
			return err
		}
		invite, err := q.LockInvitation(ctx, Hash(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Credentials
		}
		if err != nil {
			return err
		}
		account, err = q.AccountByEmail(ctx, invite.Email)
		if errors.Is(err, pgx.ErrNoRows) {
			name, err = domain.Name(name)
			if err != nil {
				return err
			}
			hash, err := s.Passwords.Hash(ctx, password)
			if err != nil {
				return err
			}
			account, err = q.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Name: name, Email: invite.Email, PasswordHash: hash})
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if current == nil || current.ID != account.ID {
				return &domain.Error{Code: "invitation_signin_required", Message: "Sign in as the invited account before accepting."}
			}
			account, err = lockCurrent(ctx, q, current.ID, current.AuthVersion)
			if err != nil {
				return err
			}
		}
		if err := q.MarkEmailVerified(ctx, db.MarkEmailVerifiedParams{ID: account.ID, EmailVerifiedAt: ptrTime(s.Now())}); err != nil {
			return err
		}
		if err := q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: invite.WorkspaceID, AccountID: account.ID, Role: invite.Role}); err != nil {
			return err
		}
		if err := q.DeleteInvitation(ctx, db.DeleteInvitationParams{WorkspaceID: invite.WorkspaceID, ID: invite.ID}); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.Principal{Account: account}, invite.WorkspaceID, "invitation.accept", invite.ID)
	})
	return account, err
}

func ptrTime(value time.Time) *time.Time { return &value }
