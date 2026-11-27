package auth

import (
	"context"
	"crypto/sha256"
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
	Tokens TokenSealer
	// Queue takes reset requests. Only a serving process has one.
	Queue *river.Client[pgx.Tx]
}

// Finish completes a sign-in, a change to how an account signs in, an invitation's acceptance or
// a password reset, in the transaction that makes it, given the account as that transaction
// leaves it. The browser session the request signs in, renews or keeps is stored there, or the
// one a reset ends is ended there, so it commits with what authorized it or neither does: a
// failure in between leaves a recovery code or authenticator code unspent, a password or
// two-factor change unapplied, an invitation or reset link unused, and the recovery codes the
// account had still valid, rather than replaced by codes the failed response never showed.
type Finish func(tx pgx.Tx, account db.Account) error

// run runs f, if there is one: a caller with no session to store, such as one making a workspace,
// passes none.
func (f Finish) run(tx pgx.Tx, account db.Account) error {
	if f == nil {
		return nil
	}
	return f(tx, account)
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

// authenticatorShaped says whether a code could be an authenticator code: six digits once
// trimmed. A recovery code never is, so a code is checked as one kind or the other, never both.
func authenticatorShaped(code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Service) checkCode(ctx context.Context, tx pgx.Tx, keys *secretbroker.Session, account *db.Account, code string) error {
	q := db.New(tx)
	code = strings.TrimSpace(code)
	if len(account.TotpSecret) == 0 {
		return domain.Credentials
	}
	if authenticatorShaped(code) {
		secret, err := keys.Open(secretbroker.Item{Kind: secretbroker.TOTP, ID: account.ID}, account.TotpSecret)
		if err != nil {
			return err
		}
		step, ok := matchTOTP(code, string(secret), s.Now(), account.TotpLastStep)
		if !ok {
			return domain.Credentials
		}
		return q.AcceptTOTPStep(ctx, db.AcceptTOTPStepParams{ID: account.ID, TotpLastStep: step})
	}
	_, err := q.ConsumeRecoveryCode(ctx, db.ConsumeRecoveryCodeParams{AccountID: account.ID, CodeHash: Hash(strings.ToUpper(code))})
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
	return s.securityEvent(ctx, tx, account.ID, RecoveryCodeUsed)
}

// VerifyCode completes a sign-in that has proved the password. Signing in again restarts the
// sign-in budgets, so besides five attempts in five minutes, each failure spends the code's
// daily allowance, which confirming identity shares.
func (s *Service) VerifyCode(ctx context.Context, id uuid.UUID, expectedVersion int64, code string, finish Finish) (_ db.Account, err error) {
	var account db.Account
	attempt := codeGuess(id, code, budget{"mfa:" + id.String(), 5, 5 * time.Minute})
	if err = s.spend(ctx, attempt); err != nil {
		return account, err
	}
	var wrong bool
	defer func() { s.settle(ctx, attempt, wrong) }()
	keys, err := s.secondFactor(ctx, id, secretbroker.Authenticate, secretbroker.TOTP)
	if err != nil {
		return account, err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
		var err error
		account, err = q.LockAccount(ctx, id)
		if err != nil {
			return err
		}
		// A credential change since the password was checked ends the pending sign-in. No code
		// was checked, so it is not a failure.
		if account.AuthVersion != expectedVersion {
			return domain.Credentials
		}
		err = s.checkCode(ctx, tx, keys, &account, code)
		wrong = errors.Is(err, domain.Credentials)
		if err != nil {
			return err
		}
		return finish.run(tx, account)
	})
	return account, err
}

// lockCurrent locks the account for a browser session's change to it, then checks that the
// session still stands. The request may have spent a long time hashing a password or queued
// behind an import, and a logout neither changes the account's version nor waits for this
// lock, so only reading the session after the wait sees one. A logout that lands after this
// check, while the few statements of the change run, stops only a change that then stores the
// session. The lock does not make a row that refers to the account wait, such as an audit event
// another transaction records while it holds a terminal session's row, which this change's
// renewal may move.
func lockCurrent(ctx context.Context, q *db.Queries, p store.Principal) (db.Account, error) {
	account, _, err := lockCurrentLineage(ctx, q, p)
	return account, err
}

// lockCurrentLineage is lockCurrent, also returning the session's lineage.
func lockCurrentLineage(ctx context.Context, q *db.Queries, p store.Principal) (db.Account, uuid.UUID, error) {
	account, err := q.LockAccountForChange(ctx, p.Account.ID)
	if err != nil {
		return account, uuid.Nil, err
	}
	lineage, err := store.AdmittedLineage(ctx, q, p)
	return account, lineage, err
}

func (s *Service) ChangePassword(ctx context.Context, p store.Principal, current Credential, password string, finish Finish) (db.Account, error) {
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
	var hash string
	hashNew := func() error {
		var err error
		hash, err = s.hashNewPassword(ctx, passwordShare{"account:" + id.String(), true}, budgets, password)
		return err
	}
	account, err := s.withCredential(ctx, p, current, hashNew, func(tx pgx.Tx, q *db.Queries, locked *db.Account) error {
		if err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: id, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.DeletePasswordReset(ctx, id); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, id, PasswordChanged); err != nil {
			return err
		}
		var err error
		*locked, err = q.AccountByID(ctx, id)
		return err
	}, finish)
	return account, credentialError(err, currentField)
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
// done to the account's sign-in budgets. finish runs in the reset's transaction.
func (s *Service) CompleteReset(ctx context.Context, token, password, ip string, finish Finish) (db.Account, error) {
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
	err = s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
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
		if err := s.securityEvent(ctx, tx, reset.AccountID, PasswordReset); err != nil {
			return err
		}
		if account, err = q.AccountByID(ctx, reset.AccountID); err != nil {
			return err
		}
		return finish.run(tx, account)
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

func (s *Service) AcceptInvitation(ctx context.Context, token, name, password string, from Source, current *store.Principal, finish Finish) (db.Account, error) {
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
		if err := store.Audit(ctx, q, store.Principal{Account: account}, invite.WorkspaceID, "invitation.accept", invite.ID); err != nil {
			return err
		}
		return finish.run(tx, account)
	})
	return account, err
}

func ptrTime(value time.Time) *time.Time { return &value }
