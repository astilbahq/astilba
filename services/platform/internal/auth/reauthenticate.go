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

// Reauthenticate renews a session's recent authentication with a fresh proof of one factor.
// The session already proved every factor the account has when it signed in, so the step-up
// asks for the strongest one alone: an account with two-factor authentication presents an
// authenticator or recovery code, and one without presents its password. A password adds
// little beside a code: a browser left signed in often fills it in by itself, while the
// authenticator stays with its owner. The request schema admits exactly one non-empty
// credential, so a client cannot believe a value was checked when it was not.
//
// The principal's account is the row at the session's version, and every change to the
// password or to two-factor authentication advances that version, so it decides which
// credential applies. lockCurrent refuses once the version has moved, so a credential change
// or logout that landed while this request waited wins. A code is checked exactly as at
// sign-in: a time step is accepted once, and a recovery code is consumed and ends every other
// session.
func (s *Service) Reauthenticate(ctx context.Context, p store.Principal, password, code string) (db.Account, error) {
	mfa := len(p.Account.TotpSecret) > 0
	if mfa && code == "" {
		return db.Account{}, domain.Invalid("code")
	}
	if !mfa && password == "" {
		return db.Account{}, domain.Invalid("password")
	}
	attempts, failures := reauthBudgets(p.Account.ID)
	budgets := []budget{attempts, failures}
	var account db.Account
	var charged bool
	var err error
	if mfa {
		account, charged, err = s.reauthenticateCode(ctx, p, code, budgets)
	} else {
		account, charged, err = s.reauthenticatePassword(ctx, p, password, budgets)
	}
	if charged && !errors.Is(err, domain.Credentials) {
		// A refund that does not land leaves one more failure counted, which only refuses sooner.
		undo, cancel := Detached(ctx)
		defer cancel()
		_ = s.Store.Queries.RefundAuthAttempt(undo, Hash(failures.scope))
	}
	return account, err
}

func (s *Service) reauthenticateCode(ctx context.Context, p store.Principal, code string, budgets []budget) (_ db.Account, _ bool, err error) {
	var account db.Account
	if err = s.refuseSpent(ctx, budgets); err != nil {
		return account, false, err
	}
	if err = s.charge(ctx, budgets); err != nil {
		return account, false, err
	}
	keys, err := s.secondFactor(ctx, p.Account.ID, secretbroker.Authenticate, secretbroker.TOTP)
	if err != nil {
		return account, true, err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		if account, err = lockCurrent(ctx, q, p); err != nil {
			return err
		}
		return s.checkCode(ctx, q, keys, &account, code)
	})
	return account, true, err
}

func (s *Service) reauthenticatePassword(ctx context.Context, p store.Principal, password string, budgets []budget) (db.Account, bool, error) {
	var account db.Account
	var charged bool
	// The hash runs outside the transaction, because it can wait for capacity and must not
	// hold the account row or a connection meanwhile.
	err := s.withPassword(ctx, passwordShare{"account:" + p.Account.ID.String(), true}, budgets, func() error {
		charged = true
		return passwordMatches(password, p.Account.PasswordHash)
	})
	if err != nil {
		return account, charged, err
	}
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		var err error
		account, err = lockCurrent(ctx, q, p)
		return err
	})
	return account, charged, err
}
