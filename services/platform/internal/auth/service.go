package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/riverqueue/river"
)

type Service struct {
	Store     *store.Store
	Passwords *Passwords
	Mail      *mailer.Mailer
	Now       func() time.Time
	// Tokens seals the tokens this service issues to itself: device tokens and reset requests.
	Tokens *secretbroker.Tokens
	// Queue takes reset requests. Only a serving process has one.
	Queue *river.Client[pgx.Tx]
}

// Hash is the stored lookup key for two kinds of value, neither a secret a person chose. Link
// and invitation tokens and recovery codes carry at least 128 random bits, so their keys cannot
// be reversed by guessing. Rate-limit scopes are labels naming an operation and an address,
// account, email or device. They are not secret, and a key can be matched by guessing its label.
// Passwords never come here: Passwords stores them with Argon2id.
func Hash(value string) []byte { hash := sha256.Sum256([]byte(value)); return hash[:] }

func (s *Service) Limit(ctx context.Context, scope string, max int32, window time.Duration) error {
	if err := s.Store.Queries.PruneAuthAttempts(ctx); err != nil {
		return err
	}
	return s.recordAttempt(ctx, s.Store.Queries, scope, max, window)
}

func (s *Service) recordAttempt(ctx context.Context, q *db.Queries, scope string, max int32, window time.Duration) error {
	count, err := q.RecordAuthAttempt(ctx, db.RecordAuthAttemptParams{ScopeHash: Hash(scope), ExpiresAt: s.Now().Add(window)})
	if err != nil {
		return err
	}
	if count > max {
		return domain.RateLimited
	}
	return nil
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

func (s *Service) checkCode(ctx context.Context, q *db.Queries, keys *secretbroker.Session, account *db.Account, code string) error {
	code = strings.TrimSpace(code)
	if len(account.TotpSecret) == 0 {
		return domain.Credentials
	}
	secret, err := keys.Open(secretbroker.Item{Kind: secretbroker.TOTP, ID: account.ID}, account.TotpSecret)
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
	if err != nil {
		return err
	}
	// A recovery code may mean a lost authenticator or a stolen account, so its use has
	// the reach of a password change: other sessions and terminals end, and automation
	// credentials, preview authority and unfinished GitHub connections stop working.
	if err = q.BumpAuthVersion(ctx, account.ID); err != nil {
		return err
	}
	account.AuthVersion++
	return nil
}
func (s *Service) VerifyCode(ctx context.Context, id uuid.UUID, expectedVersion int64, code string) (_ db.Account, err error) {
	var account db.Account
	if err = s.Limit(ctx, "mfa:"+id.String(), 5, 5*time.Minute); err != nil {
		return account, err
	}
	keys, err := s.secondFactor(ctx, id, secretbroker.Authenticate, secretbroker.TOTP)
	if err != nil {
		return account, err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = q.LockAccount(ctx, id)
		if err != nil {
			return err
		}
		if account.AuthVersion != expectedVersion {
			return domain.Credentials
		}
		return s.checkCode(ctx, q, keys, &account, code)
	})
	return account, err
}

func recoveryCodes(ctx context.Context, q *db.Queries, id uuid.UUID) ([]string, error) {
	generation := uuid.New()
	if err := q.StartRecoveryCodes(ctx, db.StartRecoveryCodesParams{ID: id, RecoveryCodesGeneration: &generation}); err != nil {
		return nil, err
	}
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

// lockCurrent locks the account for a browser session's change to it, then checks that the
// session still stands. The request may have spent a long time hashing a password or queued
// behind an import, and a logout neither changes the account's version nor waits for this
// lock, so only reading the session after the wait sees one. A logout that lands after this
// check, while the few statements of the change run, does not stop it.
func lockCurrent(ctx context.Context, q *db.Queries, p store.Principal) (db.Account, error) {
	account, err := q.LockAccount(ctx, p.Account.ID)
	if err != nil {
		return account, err
	}
	return account, store.AdmitBrowserSession(ctx, q, p)
}

// lockCredential is lockCurrent for a change to the account's own credentials, which also
// needs the session's authentication to be recent once the lock is held. The handler checked
// it before the request hashed a password or waited behind an import or another change, and
// the shorter window can lapse during either.
func lockCredential(ctx context.Context, q *db.Queries, p store.Principal) (db.Account, error) {
	account, err := lockCurrent(ctx, q, p)
	if err == nil && !p.CredentialRecent() {
		err = domain.RecentRequired
	}
	return account, err
}

func (s *Service) BeginMFA(ctx context.Context, p store.Principal) (_, _ string, err error) {
	var secret, uri string
	keys, err := s.secondFactor(ctx, p.Account.ID, secretbroker.Seal, secretbroker.TOTPPending)
	if err != nil {
		return "", "", err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		account, err := lockCurrent(ctx, q, p)
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
		blob, err := keys.Seal(secretbroker.Item{Kind: secretbroker.TOTPPending, ID: account.ID}, []byte(key.Secret()))
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
func (s *Service) ConfirmMFA(ctx context.Context, p store.Principal, code string) (_ db.Account, _ []string, err error) {
	id := p.Account.ID
	var account db.Account
	var codes []string
	if err = s.Limit(ctx, "mfa-confirm:"+id.String(), 5, 5*time.Minute); err != nil {
		return account, nil, err
	}
	keys, err := s.secondFactor(ctx, id, secretbroker.Authenticate, secretbroker.TOTPPending, secretbroker.TOTP)
	if err != nil {
		return account, nil, err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = lockCredential(ctx, q, p)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) > 0 || account.TotpPendingExpiry == nil || !account.TotpPendingExpiry.After(s.Now()) {
			return domain.Credentials
		}
		secret, err := keys.Open(secretbroker.Item{Kind: secretbroker.TOTPPending, ID: id}, account.TotpPending)
		if err != nil {
			return err
		}
		step, ok := matchTOTP(strings.TrimSpace(code), string(secret), s.Now(), -1)
		if !ok {
			return domain.Credentials
		}
		active, err := keys.Seal(secretbroker.Item{Kind: secretbroker.TOTP, ID: id}, secret)
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
func (s *Service) DisableMFA(ctx context.Context, p store.Principal) (db.Account, error) {
	id := p.Account.ID
	var account db.Account
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		current, err := lockCredential(ctx, q, p)
		if err != nil {
			return err
		}
		if len(current.TotpSecret) == 0 {
			return domain.Conflict
		}
		if _, err := q.DisableTOTP(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
			return err
		}
		account, err = q.AccountByID(ctx, id)
		return err
	})
	return account, err
}
func (s *Service) RegenerateRecovery(ctx context.Context, p store.Principal) (db.Account, []string, error) {
	id := p.Account.ID
	var account db.Account
	var codes []string
	err := s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = lockCredential(ctx, q, p)
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
func (s *Service) ChangePassword(ctx context.Context, p store.Principal, password string) (db.Account, error) {
	id := p.Account.ID
	var account db.Account
	if err := domain.Password(password); err != nil {
		return account, err
	}
	// A session that has already ended pays nothing. lockCurrent still decides, after the hash.
	if err := store.AdmitBrowserSession(ctx, s.Store.Queries, p); err != nil {
		return account, err
	}
	budgets := []budget{{"password-change:" + id.String(), 5, attemptWindow}}
	if err := s.refuseSpent(ctx, budgets); err != nil {
		return account, err
	}
	hash, err := s.hashNewPassword(ctx, passwordShare{"account:" + id.String(), true}, budgets, password)
	if err != nil {
		return account, err
	}
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		if _, err := lockCredential(ctx, q, p); err != nil {
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

func (s *Service) ValidateReset(ctx context.Context, token, ip string) error {
	if err := s.Limit(ctx, "reset-validate:ip:"+ip, 60, 15*time.Minute); err != nil {
		return err
	}
	_, err := s.Store.Queries.PeekPasswordReset(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credentials
	}
	return err
}

// CompleteReset proves control of the account's email, so it also lifts what guessing has
// done to the account's sign-in budgets.
func (s *Service) CompleteReset(ctx context.Context, token, password, ip string) (db.Account, error) {
	var account db.Account
	if err := domain.Password(password); err != nil {
		return account, err
	}
	budgets := []budget{{"reset-complete:ip:" + ip, 20, attemptWindow}}
	if err := s.refuseSpent(ctx, budgets); err != nil {
		return account, err
	}
	peek, err := s.Store.Queries.PeekPasswordReset(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return account, s.refuseLink(ctx, budgets)
	}
	if err != nil {
		return account, err
	}
	hash, err := s.hashNewPassword(ctx, passwordShare{"account:" + peek.AccountID.String(), true}, budgets, password)
	if err != nil {
		return account, err
	}
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		locked, err := q.LockAccount(ctx, peek.AccountID)
		if err != nil {
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
		if err := q.DeletePasswordReset(ctx, reset.AccountID); err != nil {
			return err
		}
		if err := s.clearLoginFailures(ctx, q, locked.Email, ip); err != nil {
			return err
		}
		account, err = q.AccountByID(ctx, reset.AccountID)
		return err
	})
	return account, err
}

func (s *Service) ValidateInvitation(ctx context.Context, token, ip string) error {
	if err := s.Limit(ctx, "invite-validate:ip:"+ip, 60, 15*time.Minute); err != nil {
		return err
	}
	_, err := s.Store.Queries.PeekInvitation(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credentials
	}
	return err
}

func (s *Service) AcceptInvitation(ctx context.Context, token, name, password string, from Source, current *store.Principal) (db.Account, error) {
	var account db.Account
	budgets := []budget{{"invite:" + from.Key, 20, attemptWindow}}
	if err := s.refuseSpent(ctx, budgets); err != nil {
		return account, err
	}
	peek, err := s.Store.Queries.PeekInvitation(ctx, Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return account, s.refuseLink(ctx, budgets)
	}
	if err != nil {
		return account, err
	}
	// The hash runs before the workspace's write lock, because it can wait for capacity.
	var hash string
	_, err = s.Store.Queries.AccountByEmail(ctx, peek.Email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if name, err = domain.Name(name); err != nil {
			return account, err
		}
		if err = domain.Password(password); err != nil {
			return account, err
		}
		hash, err = s.hashNewPassword(ctx, passwordShare{from.principal(), true}, budgets, password)
	case err == nil:
		err = s.charge(ctx, budgets)
	}
	if err != nil {
		return account, err
	}
	err = s.Store.WorkspaceWriteTransaction(ctx, peek.WorkspaceID, func(tx pgx.Tx) error {
		q := db.New(tx)
		invite, err := q.LockInvitation(ctx, Hash(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Credentials
		}
		if err != nil {
			return err
		}
		// Joining grants access, so a hosted workspace needs active management. The invitation is
		// kept, so it can still be accepted before it expires if the subscription is renewed.
		if err = s.Store.ManagementAdmission(ctx, tx, invite.WorkspaceID); err != nil {
			return err
		}
		account, err = q.AccountByEmail(ctx, invite.Email)
		if errors.Is(err, pgx.ErrNoRows) {
			// The account the invitation found has gone since, so no password was set for this.
			if hash == "" {
				return domain.Conflict
			}
			account, err = q.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Name: name, Email: invite.Email, PasswordHash: hash})
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if current == nil || current.Account.ID != account.ID {
				return &domain.Error{Code: "invitation_signin_required", Message: "Sign in as the invited account before accepting."}
			}
			account, err = lockCurrent(ctx, q, *current)
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

func (s *Service) AcknowledgeRecoveryCodes(ctx context.Context, p store.Principal, generation uuid.UUID) error {
	if !p.MFA {
		return domain.MFARequired
	}
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		account, err := lockCurrent(ctx, q, p)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) == 0 || account.RecoveryCodesGeneration == nil || *account.RecoveryCodesGeneration != generation {
			return domain.Conflict
		}
		return q.MarkRecoveryCodesSaved(ctx, account.ID)
	})
}
