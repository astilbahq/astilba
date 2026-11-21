package auth

import (
	"context"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reauthentication spends reauthAttempts in each attemptWindow, successful or not, from the
// account's holders of live sessions. Signing in again restarts recent authentication
// without spending them.
const reauthAttempts = 5

func reauthBudget(id uuid.UUID) budget {
	return budget{"reauth:" + id.String(), reauthAttempts, attemptWindow}
}

// The account's daily allowance of failed guesses at a credential that a short-window budget
// alone would leave open to patient guessing: an authenticator code at sign-in or when
// confirming identity, and the password when confirming identity on an account without
// two-factor authentication. Sign-in and confirmation share it, so whoever knows the password
// and whoever holds a session cannot add their guesses together, and signing in again, which
// restarts the sign-in budgets, does not restart it. That bounds guessing at the six-digit code
// to credentialDailyFailures in each daily window, a chance of about 0.006% with three time
// steps accepted, however the attempts are spread.
//
// Every guess spends it before its credential is checked, so concurrent guesses cannot exceed
// it together, and a spent allowance refuses before any check, so a refusal never tells a right
// code from a wrong one. A guess that does not fail on its credential gets its failure back, so
// signing in and confirming often cost nothing. Like every budget here the window is fixed, not
// sliding.
//
// Only someone who knows the password or holds one of the account's sessions can spend it, but
// spending it does refuse the owner's own authenticator for the rest of the window. A recovery
// code never spends it and is still accepted once it is spent: it carries 128 random bits, so a
// daily bound adds nothing against guessing, and it is the owner's way back in meanwhile, as
// is an operator's ResetMFA. A password reset does not restore it: someone who has the
// mailbox as well as the password could otherwise restore it with every reset link.
const (
	credentialDailyFailures = 20
	credentialFailureWindow = 24 * time.Hour
)

func credentialFailures(id uuid.UUID) budget {
	return budget{"credential-failures:" + id.String(), credentialDailyFailures, credentialFailureWindow}
}

// guess is one check of a credential, with the budgets it spends before the check. When it
// spends the account's credential failure budget, that comes last, so concurrent charges take
// its row lock after any other.
type guess struct {
	budgets []budget
	// failures is the credential failure budget, if the guess spends it.
	failures *budget
}

func failingGuess(id uuid.UUID, attempts ...budget) guess {
	failures := credentialFailures(id)
	return guess{append(attempts, failures), &failures}
}

// codeGuess is a code's budgets: always the short-window ones, and the daily failure budget
// unless the code cannot be an authenticator code, which leaves only a recovery code.
func codeGuess(id uuid.UUID, code string, attempts ...budget) guess {
	if !authenticatorShaped(code) {
		return guess{budgets: attempts}
	}
	return failingGuess(id, attempts...)
}

// spend charges every budget of the guess, refusing without writing if any is spent.
func (s *Service) spend(ctx context.Context, g guess) error {
	if err := s.refuseSpent(ctx, g.budgets); err != nil {
		return err
	}
	return s.charge(ctx, g.budgets)
}

// settle gives a charged guess its failure back unless its credential was wrong. A refund that
// does not land leaves one more failure counted, which only refuses sooner.
func (s *Service) settle(ctx context.Context, g guess, wrong bool) {
	if g.failures == nil || wrong {
		return
	}
	undo, cancel := Detached(ctx)
	defer cancel()
	_ = s.Store.Queries.RefundAuthAttempt(undo, Hash(g.failures.scope))
}

// Credential is one fresh credential from the holder of a session, the strongest the account
// has: an authenticator or recovery code with two-factor authentication, otherwise the
// password. The session already proved every factor when it signed in, so one is enough. A
// password adds little beside a code: a browser left signed in often fills it in by itself,
// while the authenticator stays with its owner. Request schemas admit exactly one non-empty
// credential, so a client cannot believe a value was checked when it was not.
type Credential struct {
	Password, Code string
}

// withCredential runs change in a transaction holding the account, once the session's holder
// has presented a current credential. It is what stands between a stolen session and the
// account: confirming identity, and changing the password or two-factor authentication.
//
// The principal's account is the row at the session's version, and every change to the
// password or to two-factor authentication advances that version, so it decides which
// credential applies. lockCurrent refuses once the version has moved, so a credential change
// or logout that landed while this request waited wins. A code is checked exactly as at
// sign-in: a time step is accepted once, and a recovery code is consumed and ends every other
// session. A password is hashed before the transaction, because the hash can wait for
// capacity and must not hold the account row or a connection meanwhile. prepare, if any, runs
// after the password and before the transaction, for the same reason.
//
// Every attempt spends the reauthentication budget and, unless it is a recovery code, the
// account's credential failure budget, so a stolen session cannot guess more often by choosing
// which change to attempt. One that does not fail on its credential gets its failure back.
func (s *Service) withCredential(ctx context.Context, p store.Principal, c Credential, prepare func() error, change func(pgx.Tx, *db.Queries, *db.Account) error) error {
	mfa := len(p.Account.TotpSecret) > 0
	if mfa && c.Code == "" || !mfa && c.Password == "" {
		return errCredentialKind
	}
	attempt := failingGuess(p.Account.ID, reauthBudget(p.Account.ID))
	if mfa {
		attempt = codeGuess(p.Account.ID, c.Code, reauthBudget(p.Account.ID))
	}
	var charged, wrong bool
	err := func() (err error) {
		if mfa {
			if err := s.spend(ctx, attempt); err != nil {
				return err
			}
			charged = true
		} else if err := s.withPassword(ctx, passwordShare{"account:" + p.Account.ID.String(), true}, attempt.budgets, func() error {
			charged = true
			err := passwordMatches(c.Password, p.Account.PasswordHash)
			wrong = errors.Is(err, domain.Credentials)
			return err
		}); err != nil {
			return err
		}
		if prepare != nil {
			if err := prepare(); err != nil {
				return err
			}
		}
		var keys *secretbroker.Session
		if mfa {
			if keys, err = s.secondFactor(ctx, p.Account.ID, secretbroker.Authenticate, secretbroker.TOTP); err != nil {
				return err
			}
			defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
		}
		return s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
			account, err := lockCurrent(ctx, q, p)
			if err != nil {
				return err
			}
			if mfa {
				if err := s.checkCode(ctx, tx, keys, &account, c.Code); err != nil {
					wrong = errors.Is(err, domain.Credentials)
					return err
				}
			}
			return change(tx, q, &account)
		})
	}()
	if charged {
		s.settle(ctx, attempt, wrong)
	}
	return err
}

// errCredentialKind is a credential of the wrong kind for the account, which happens when a
// client's view of whether the account has two-factor authentication is out of date.
var errCredentialKind = errors.New("the account needs another kind of credential")

// credentialError names the request field that held a credential of the wrong kind: the
// credential itself when confirming identity, or `current` when it came with a change.
func credentialError(err error, field func() string) error {
	if errors.Is(err, errCredentialKind) {
		return domain.Invalid(field())
	}
	return err
}

func currentField() string { return "current" }

// Reauthenticate renews a session's recent authentication with a current credential.
func (s *Service) Reauthenticate(ctx context.Context, p store.Principal, c Credential) (db.Account, error) {
	var account db.Account
	err := s.withCredential(ctx, p, c, nil, func(_ pgx.Tx, _ *db.Queries, a *db.Account) error {
		account = *a
		return nil
	})
	return account, credentialError(err, func() string {
		if len(p.Account.TotpSecret) > 0 {
			return "code"
		}
		return "password"
	})
}
