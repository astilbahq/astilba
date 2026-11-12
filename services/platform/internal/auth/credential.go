package auth

import (
	"context"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reauthentication budgets. Every attempt spends one of the five in attemptWindow and one
// of a daily allowance of failures, both before its credential is checked, so concurrent
// attempts cannot exceed either together: a holder of a stolen session can check at most
// reauthDailyFailures guesses in each daily window at the account's authenticator code,
// wherever they come from. Like every budget here, the windows are fixed, not sliding. An
// attempt that does not fail on its credential gets its failure back, so confirming often
// costs nothing. Only a holder of one of the account's live sessions can spend either, and
// signing in again restarts recent authentication without them.
const (
	reauthAttempts      = 5
	reauthDailyFailures = 20
	reauthFailureWindow = 24 * time.Hour
)

func reauthBudgets(id uuid.UUID) (attempts, failures budget) {
	return budget{"reauth:" + id.String(), reauthAttempts, attemptWindow},
		budget{"reauth-failures:" + id.String(), reauthDailyFailures, reauthFailureWindow}
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
// Every attempt spends the reauthentication budgets, so a stolen session cannot guess more
// often by choosing which change to attempt. One that does not fail on its credential gets its
// failure back.
func (s *Service) withCredential(ctx context.Context, p store.Principal, c Credential, prepare func() error, change func(pgx.Tx, *db.Queries, *db.Account) error) error {
	mfa := len(p.Account.TotpSecret) > 0
	if mfa && c.Code == "" || !mfa && c.Password == "" {
		return errCredentialKind
	}
	attempts, failures := reauthBudgets(p.Account.ID)
	budgets := []budget{attempts, failures}
	var charged, wrong bool
	err := func() error {
		if mfa {
			if err := s.refuseSpent(ctx, budgets); err != nil {
				return err
			}
			if err := s.charge(ctx, budgets); err != nil {
				return err
			}
			charged = true
		} else if err := s.withPassword(ctx, passwordShare{"account:" + p.Account.ID.String(), true}, budgets, func() error {
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
		return s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
			account, err := lockCurrent(ctx, q, p)
			if err != nil {
				return err
			}
			if mfa {
				if err := s.checkCode(ctx, tx, &account, c.Code); err != nil {
					wrong = errors.Is(err, domain.Credentials)
					return err
				}
			}
			return change(tx, q, &account)
		})
	}()
	if charged && !wrong {
		// A refund that does not land leaves one more failure counted, which only refuses sooner.
		undo, cancel := Detached(ctx)
		defer cancel()
		_ = s.Store.Queries.RefundAuthAttempt(undo, Hash(failures.scope))
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
