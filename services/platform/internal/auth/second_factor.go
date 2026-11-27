package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// secondFactor gives out the keys to one account's own second factor, recorded first. It runs
// before the transaction that uses them.
func (s *Service) secondFactor(ctx context.Context, account uuid.UUID, purpose secretbroker.Purpose, kinds ...secretbroker.Kind) (*secretbroker.Session, error) {
	items := []secretbroker.Item{}
	for _, kind := range kinds {
		items = append(items, secretbroker.Item{Kind: kind, ID: account})
	}
	return s.Store.Broker.Begin(ctx, secretbroker.Request{Purpose: purpose, Principal: secretbroker.Account(account), Items: items})
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

// MFASetupElsewhere refuses to begin an enrolment while another signed-in browser session's
// enrolment lasts.
var MFASetupElsewhere = &domain.Error{Code: "mfa_setup_elsewhere", Message: "Two-factor authentication is being set up in another signed-in session. Finish it or sign out there, sign that session out under Signed-in sessions, or wait up to 10 minutes."}

// pendingEnrolment refuses a session that has no unexpired enrolment of its own to confirm, such as
// one another session began, as it refuses a lapsed one. The account must not have an authenticator.
func (s *Service) pendingEnrolment(account db.Account, lineage uuid.UUID) error {
	// The new authenticator's code is not the credential that confirms identity, so an
	// enrolment that has lapsed is refused as input to try again.
	if len(account.TotpSecret) > 0 || account.TotpPendingExpiry == nil || !account.TotpPendingExpiry.After(s.Now()) || account.TotpPendingLineage == nil || *account.TotpPendingLineage != lineage {
		return domain.Invalid("code")
	}
	return nil
}

// BeginMFA starts an enrolment bound to the browser session that asks, so that no other session,
// such as an old one someone else holds, can replace the secret its holder scanned or confirm it.
// The session may start again, which replaces its own.
func (s *Service) BeginMFA(ctx context.Context, p store.Principal) (_, _ string, err error) {
	var secret, uri string
	keys, err := s.secondFactor(ctx, p.Account.ID, secretbroker.Seal, secretbroker.TOTPPending)
	if err != nil {
		return "", "", err
	}
	defer func() { keys.Close(ctx, secretbroker.Done(err)) }()
	err = s.Store.Transaction(ctx, func(q *db.Queries) error {
		// The enrolment is bound to the session's lineage, which stays the same when it renews
		// its token, as confirming credentials does.
		account, lineage, err := lockCurrentLineage(ctx, q, p)
		if err != nil {
			return err
		}
		if len(account.TotpSecret) > 0 {
			return domain.Conflict
		}
		if other := account.TotpPendingLineage; other != nil && *other != lineage && account.TotpPendingExpiry != nil && account.TotpPendingExpiry.After(s.Now()) {
			// An enrolment whose session has signed out is no one's, so it gives way.
			live, err := q.SessionLineageLive(ctx, *other)
			if err != nil {
				return err
			}
			if live {
				return MFASetupElsewhere
			}
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "Astilba", AccountName: account.Email, Period: 30, SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return err
		}
		blob, err := keys.Seal(ctx, q, secretbroker.Item{Kind: secretbroker.TOTPPending, ID: account.ID}, []byte(key.Secret()))
		if err != nil {
			return err
		}
		expires := s.Now().Add(10 * time.Minute)
		if err = q.SetPendingTOTP(ctx, db.SetPendingTOTPParams{ID: account.ID, TotpPending: blob, TotpPendingExpiry: &expires, TotpPendingLineage: &lineage}); err != nil {
			return err
		}
		secret, uri = key.Secret(), key.URL()
		return nil
	})
	return secret, uri, err
}

// ConfirmMFA turns on two-factor authentication with the authenticator BeginMFA gave the same
// browser session, once the session's holder has given the password. Without it a stolen session
// could enrol its own authenticator, and the account's owner could not sign in again even after
// resetting the password.
func (s *Service) ConfirmMFA(ctx context.Context, p store.Principal, current Credential, code string, finish Finish) (_ db.Account, _ []string, err error) {
	id := p.Account.ID
	var account db.Account
	var codes []string
	// Only the session that began the enrolment may try to confirm it, so no other session can
	// spend the account's confirmation limit and keep the owner from confirming, and its
	// attempts here spend no credential budget either. The refusal says only that this session
	// has nothing to confirm, which its holder knows. A session that a credential change or
	// logout has ended is refused as such first. Both are read in one snapshot: a logout or
	// password change that ends the enrolment with the session, committing between two reads,
	// would show a live session with nothing to confirm. The check is made again under the
	// account's lock; the session's lineage stays the same, as it is fixed with its token.
	var lineage uuid.UUID
	var enrolling db.Account
	err = s.Store.ReadSnapshot(ctx, func(q *db.Queries) (err error) {
		if lineage, err = store.AdmittedLineage(ctx, q, p); err != nil {
			return err
		}
		enrolling, err = q.AccountByID(ctx, id)
		return store.IfMissing(err, domain.Unauthenticated)
	})
	if err != nil {
		return account, nil, err
	}
	if err = s.pendingEnrolment(enrolling, lineage); err != nil {
		return account, nil, err
	}
	if err = s.Limit(ctx, "mfa-confirm:"+id.String(), 5, 5*time.Minute); err != nil {
		return account, nil, err
	}
	var keys *secretbroker.Session
	defer func() {
		if keys != nil {
			keys.Close(ctx, secretbroker.Done(err))
		}
	}()
	openKeys := func() (err error) {
		keys, err = s.secondFactor(ctx, id, secretbroker.Authenticate, secretbroker.TOTPPending, secretbroker.TOTP)
		return err
	}
	account, err = s.withCredential(ctx, p, current, openKeys, func(tx pgx.Tx, q *db.Queries, locked *db.Account) error {
		if err := s.pendingEnrolment(*locked, lineage); err != nil {
			return err
		}
		// A wrong code for the new authenticator is refused as input to try again.
		secret, err := keys.Open(secretbroker.Item{Kind: secretbroker.TOTPPending, ID: id}, locked.TotpPending)
		if err != nil {
			return err
		}
		step, ok := matchTOTP(strings.TrimSpace(code), string(secret), s.Now(), -1)
		if !ok {
			return domain.Invalid("code")
		}
		active, err := keys.Seal(ctx, q, secretbroker.Item{Kind: secretbroker.TOTP, ID: id}, secret)
		if err != nil {
			return err
		}
		if _, err = q.ActivateTOTP(ctx, db.ActivateTOTPParams{ID: id, TotpLastStep: step, TotpSecret: active}); err != nil {
			return err
		}
		// Failed guesses at an earlier authenticator say nothing about this one.
		if err = q.ClearAuthAttempts(ctx, Hash(codeFailures(id).scope)); err != nil {
			return err
		}
		codes, err = recoveryCodes(ctx, q, id)
		if err != nil {
			return err
		}
		if err = s.securityEvent(ctx, tx, id, MFAEnabled); err != nil {
			return err
		}
		*locked, err = q.AccountByID(ctx, id)
		return err
	}, finish)
	return account, codes, credentialError(err, currentField)
}
func (s *Service) DisableMFA(ctx context.Context, p store.Principal, current Credential, finish Finish) (db.Account, error) {
	id := p.Account.ID
	account, err := s.withCredential(ctx, p, current, nil, func(tx pgx.Tx, q *db.Queries, locked *db.Account) error {
		if len(locked.TotpSecret) == 0 {
			return domain.Conflict
		}
		if _, err := q.DisableTOTP(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, id, MFADisabled); err != nil {
			return err
		}
		var err error
		*locked, err = q.AccountByID(ctx, id)
		return err
	}, finish)
	return account, credentialError(err, currentField)
}
func (s *Service) RegenerateRecovery(ctx context.Context, p store.Principal, current Credential, finish Finish) (db.Account, []string, error) {
	id := p.Account.ID
	var codes []string
	account, err := s.withCredential(ctx, p, current, nil, func(tx pgx.Tx, q *db.Queries, locked *db.Account) error {
		if len(locked.TotpSecret) == 0 {
			return domain.Conflict
		}
		var err error
		codes, err = recoveryCodes(ctx, q, id)
		if err != nil {
			return err
		}
		if err := q.BumpAuthVersion(ctx, id); err != nil {
			return err
		}
		if err := s.securityEvent(ctx, tx, id, RecoveryCodesRegenerated); err != nil {
			return err
		}
		*locked, err = q.AccountByID(ctx, id)
		return err
	}, finish)
	return account, codes, credentialError(err, currentField)
}

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
