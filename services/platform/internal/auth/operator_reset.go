package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// MFAReset is an operator's reset of an account's two-factor authentication.
const MFAReset = "mfa_reset"

// maxResetReason is in bytes, so the reason's line in the notice stays within what SMTP allows.
const maxResetReason = 500

// ErrMailUnconfigured refuses a reset that the holder could not complete: they get back in only
// through an emailed password reset.
var ErrMailUnconfigured = errors.New("email is not configured, so the account holder could not reset their password afterwards; configure SMTP first")

// ResetMFA is the operator's way back into an account whose authenticator is lost, with no
// recovery codes left, or was enrolled by someone else. Nothing a session can do removes an
// authenticator without a current code, and a password reset leaves it in place.
//
// It turns two-factor authentication off, deletes the recovery codes and replaces the password
// with one nobody knows, so every session, terminal and automation grant ends and the holder
// signs in again only after resetting the password by email. Replacing the password matters as
// much as the authenticator: whoever enrolled a foreign authenticator knew the password, and the
// operator cannot tell a holder who lost their phone from someone who phished the password and
// asked. Sign-in with the old password then fails as any wrong password does, and a reset link
// sent before ends, as at any other password change. The reason is kept with the account
// security event, which emails it to the holder.
func (s *Service) ResetMFA(ctx context.Context, email, reason string) (uuid.UUID, error) {
	if s.Mail == nil || !s.Mail.Configured() {
		return uuid.Nil, ErrMailUnconfigured
	}
	address, err := domain.Email(email)
	if err != nil {
		return uuid.Nil, err
	}
	reason, err = resetReason(reason)
	if err != nil {
		return uuid.Nil, err
	}
	// The hash runs before the transaction, so the account row is not held while it does. The
	// password itself is never kept or shown.
	hash, err := s.Passwords.Hash(rand.Text() + rand.Text())
	if err != nil {
		return uuid.Nil, err
	}
	var account, event uuid.UUID
	err = s.Store.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
		found, err := q.AccountByEmail(ctx, address)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		}
		if err != nil {
			return err
		}
		locked, err := q.LockAccount(ctx, found.ID)
		if err != nil {
			return err
		}
		account = locked.ID
		// An account without two-factor authentication can sign in with its password already,
		// and the operator has nothing to reset.
		if len(locked.TotpSecret) == 0 {
			return domain.Conflict
		}
		if _, err := q.DisableTOTP(ctx, account); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, account); err != nil {
			return err
		}
		if err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: account, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.DeletePasswordReset(ctx, account); err != nil {
			return err
		}
		event, err = s.recordSecurityEvent(ctx, tx, account, MFAReset, pgtype.Text{String: reason, Valid: true})
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	// The reason stays with the event, where its retention is the event's, and out of the log.
	slog.Info("two-factor authentication reset by the operator", "account", account, "event", event)
	return account, nil
}

// resetReason is the operator's reason, as the holder will read it in their notice: one line of
// printable text.
func resetReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxResetReason || !utf8.ValidString(reason) {
		return "", domain.Invalid("reason")
	}
	for _, r := range reason {
		// Only letters, marks, numbers, punctuation, symbols and spaces: line and paragraph
		// separators, controls and formatting characters such as direction overrides could make
		// the notice say something the operator did not write.
		if !unicode.IsGraphic(r) {
			return "", domain.Invalid("reason")
		}
	}
	return reason, nil
}
