package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/diagnostic"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
)

// Changes to how an account signs in. Each is kept as an account security event and emailed to
// the account holder, so that whoever holds the account learns of a change they did not make.
const (
	PasswordChanged          = "password_changed"
	PasswordReset            = "password_reset"
	MFAEnabled               = "mfa_enabled"
	MFADisabled              = "mfa_disabled"
	RecoveryCodesRegenerated = "recovery_codes_regenerated"
	RecoveryCodeUsed         = "recovery_code_used"
)

const (
	// SecurityMailQueue is apart from reset mail, which anyone can request for any address, so
	// that requests for resets cannot delay these notices.
	SecurityMailQueue = "security_mail"
	// A notice a day late still tells the account holder something they need to know.
	securityMailPatience = 24 * time.Hour
	securityMailTimeout  = 25 * time.Second
	// Enough to outlast securityMailPatience, which is what ends the retries.
	securityMailAttempts = 40
	securityMailMaxDelay = time.Hour
)

// securityEvent records a change to how the account signs in and queues its notice in the same
// transaction, so the notice is sent exactly when the change commits.
func (s *Service) securityEvent(ctx context.Context, tx pgx.Tx, account uuid.UUID, kind string) error {
	_, err := s.recordSecurityEvent(ctx, tx, account, kind, pgtype.Text{})
	return err
}

// recordSecurityEvent is securityEvent with a reason, which only an operator's reset has. It
// returns the event's ID.
func (s *Service) recordSecurityEvent(ctx context.Context, tx pgx.Tx, account uuid.UUID, kind string, reason pgtype.Text) (uuid.UUID, error) {
	id := uuid.New()
	if err := db.New(tx).InsertAccountSecurityEvent(ctx, db.InsertAccountSecurityEventParams{ID: id, AccountID: account, Kind: kind, Reason: reason, CreatedAt: s.Now()}); err != nil {
		return uuid.Nil, err
	}
	_, err := s.Queue.InsertTx(ctx, tx, SecurityNoticeArgs{Event: id}, nil)
	return id, err
}

// SecurityNoticeArgs names the event to tell the account holder about. The worker reads the
// address when it sends, so the job holds nothing personal.
type SecurityNoticeArgs struct {
	Event uuid.UUID `json:"event"`
}

func (SecurityNoticeArgs) Kind() string { return "platform_security_notice_v1" }

func (SecurityNoticeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: SecurityMailQueue, MaxAttempts: securityMailAttempts}
}

// SecurityNoticeWorker emails account holders about changes to how their account signs in. Its
// fields are set when the server is assembled. The River client always knows this worker,
// because a client that finds a stuck job of a kind it does not know discards it.
type SecurityNoticeWorker struct {
	river.WorkerDefaults[SecurityNoticeArgs]
	Accounts *Service
	Origin   string
	Log      *slog.Logger
}

func (w *SecurityNoticeWorker) Timeout(*river.Job[SecurityNoticeArgs]) time.Duration {
	return securityMailTimeout
}

func (w *SecurityNoticeWorker) NextRetry(job *river.Job[SecurityNoticeArgs]) time.Time {
	delay := securityMailMaxDelay
	if job.Attempt < 9 {
		delay = 15 * time.Second << (job.Attempt - 1)
	}
	return time.Now().Add(delay)
}

func (w *SecurityNoticeWorker) Work(ctx context.Context, job *river.Job[SecurityNoticeArgs]) error {
	s := w.Accounts
	// River stamps jobs with the wall clock, so patience is measured on it too.
	if time.Since(job.CreatedAt) > securityMailPatience {
		w.Log.Error("account security notice abandoned", "job", job.ID, "attempts", job.Attempt-1)
		return river.JobCancel(errors.New("account security notice abandoned"))
	}
	notice, err := s.Store.Queries.AccountSecurityNotice(ctx, job.Args.Event)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("account security event not found"))
	}
	if err != nil {
		return err
	}
	if !s.Mail.Configured() {
		w.Log.Warn("account security notice not sent: email is not configured", "job", job.ID)
		return river.JobCancel(errors.New("email is not configured"))
	}
	subject, body := securityNotice(notice, w.Origin)
	// A relay that failed may succeed later, and a notice that might arrive twice is better than
	// one that never arrives, so every failure is retried until the patience runs out.
	if err := s.Mail.Send(ctx, notice.Email, subject, body); err != nil {
		w.Log.Warn("account security notice could not be sent", "job", job.ID, "attempt", job.Attempt, diagnostic.Failure(err))
		return err
	}
	return nil
}

// securityNotice is a notice's subject and body.
func securityNotice(notice db.AccountSecurityNoticeRow, origin string) (subject, body string) {
	when := " on " + notice.CreatedAt.UTC().Format("2 January 2006 at 15:04 UTC") + ".\n\n"
	if notice.Kind == MFAReset {
		// The holder has to act whoever asked for the reset, since the password went with it.
		return "Two-factor authentication was reset", "The operator of this Astilba installation turned off two-factor authentication " +
			"for your Astilba account, deleted its recovery codes and replaced its password" + when +
			"The reason they gave: " + notice.Reason.String + "\n\n" +
			"Every session and automation token for your account has ended. To sign in again, choose a new password at " +
			origin + "/reset, then set up two-factor authentication again. If you did not ask for this, tell the operator " +
			"and your workspace owners."
	}
	subject, change, remedy := securityNoticeText(notice.Kind)
	return subject, change + when + "If this was you, there is nothing to do. If it was not, someone else may have access to your account: " +
		remedy + " at " + origin + "/reset and tell your workspace owners."
}

// securityNoticeText is a notice's subject, what changed, and what to do if it was not the
// account holder.
func securityNoticeText(kind string) (subject, change, remedy string) {
	const reset = "reset your password"
	switch kind {
	case PasswordChanged:
		return "Your Astilba password was changed", "The password for your Astilba account was changed", reset
	case PasswordReset:
		return "Your Astilba password was reset", "The password for your Astilba account was reset with an emailed link", reset
	case MFAEnabled:
		// The authenticator outlasts a reset, so a reset alone does not give the account back.
		return "Two-factor authentication is on", "Two-factor authentication was turned on for your Astilba account",
			"whoever did this knows your password and holds the authenticator. Resetting your password ends their sessions but does not remove it, so " + reset
	case MFADisabled:
		return "Two-factor authentication is off", "Two-factor authentication was turned off for your Astilba account", reset
	case RecoveryCodesRegenerated:
		return "New Astilba recovery codes", "New recovery codes were made for your Astilba account, and the old ones no longer work", reset
	case RecoveryCodeUsed:
		return "An Astilba recovery code was used", "A recovery code was used to confirm your identity on your Astilba account, and it no longer works", reset
	}
	return "Your Astilba account changed", "How your Astilba account signs in was changed", reset
}
