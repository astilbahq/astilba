package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/diagnostic"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const (
	// ResetMailQueue is separate so that anyone can name emails without delaying deployments.
	ResetMailQueue    = "reset_mail"
	resetMailIdentity = "reset-mail:v1"
	// Mail arriving after this is more confusing than useful, and whoever asked can ask again.
	resetMailPatience = 15 * time.Minute
	resetLinkLifetime = 30 * time.Minute
	// One attempt spans a dial and a full SMTP session, each with 10 seconds.
	resetMailTimeout = 25 * time.Second
	// Enough to outlast resetMailPatience, which is what ends the retries.
	resetMailAttempts = 25
	resetMailMaxDelay = 5 * time.Minute
)

// ResetMailArgs holds one reset request. The email is sealed because it is whatever the requester
// typed, which may belong to someone with no account, and finished jobs stay in the table.
type ResetMailArgs struct {
	Email []byte `json:"email"`
}

func (ResetMailArgs) Kind() string { return "platform_reset_mail_v1" }

func (ResetMailArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: ResetMailQueue, MaxAttempts: resetMailAttempts}
}

// Detached gives work its own time however its caller ended: undoing a failed send, whose failure
// may be the caller's deadline or hang-up, or finishing something already paid for.
func Detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// RequestReset only records the request, doing the same work whatever was typed. Nothing here
// reads an account, so neither its time nor its answer can depend on whether one exists.
func (s *Service) RequestReset(ctx context.Context, email string, from Source) error {
	if err := s.charge(ctx, sourceBudgets("reset", from, 20)); err != nil {
		return err
	}
	email, err := domain.Email(email)
	if err != nil {
		return nil
	}
	sealed, err := s.Store.Keys.Seal([]byte(email), resetMailIdentity)
	if err != nil {
		return err
	}
	ctx, cancel := Detached(ctx)
	defer cancel()
	_, err = s.Queue.Insert(ctx, ResetMailArgs{sealed}, nil)
	return err
}

// ResetMailWorker sends the links for reset requests, after the response. Its fields are set when
// the server is assembled. The River client always knows this worker, because a client that finds
// a stuck job of a kind it does not know discards it.
type ResetMailWorker struct {
	river.WorkerDefaults[ResetMailArgs]
	Accounts *Service
	Origin   string
	Log      *slog.Logger
}

func (w *ResetMailWorker) Timeout(*river.Job[ResetMailArgs]) time.Duration { return resetMailTimeout }

func (w *ResetMailWorker) NextRetry(job *river.Job[ResetMailArgs]) time.Time {
	delay := resetMailMaxDelay
	if job.Attempt < 5 {
		delay = 15 * time.Second << (job.Attempt - 1)
	}
	return time.Now().Add(delay)
}

func (w *ResetMailWorker) Work(ctx context.Context, job *river.Job[ResetMailArgs]) error {
	s := w.Accounts
	if s.Now().Sub(job.CreatedAt) > resetMailPatience {
		w.Log.Error("password reset mail abandoned", "job", job.ID, "attempts", job.Attempt-1)
		return river.JobCancel(errors.New("password reset mail abandoned"))
	}
	email, err := s.Store.Keys.Open(job.Args.Email, resetMailIdentity)
	if err != nil {
		w.Log.Error("password reset request could not be opened", "job", job.ID)
		return river.JobCancel(errors.New("password reset request could not be opened"))
	}
	account, err := s.Store.Queries.AccountByEmail(ctx, string(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return w.unprepared(job, err)
	}
	// A retry must not replace a link sent since this request, which someone may be using. River
	// stamps a job with the time in the process that queues it, as saveResetLink does a link.
	expiry, err := s.Store.Queries.PasswordResetExpiry(ctx, account.ID)
	if err == nil && expiry.Add(-resetLinkLifetime).After(job.CreatedAt) {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w.unprepared(job, err)
	}
	token, err := randtoken.New()
	if err != nil {
		return w.unprepared(job, err)
	}
	// Only an account's mail is charged, so asking about an email with no account leaves nothing
	// behind for when it gets one. The charge comes first so that concurrent jobs cannot exceed it.
	scope := "reset:email:" + account.Email
	err = s.charge(ctx, []budget{{scope, 3, attemptWindow}})
	if errors.Is(err, domain.RateLimited) {
		return nil
	}
	if err != nil {
		return w.unprepared(job, err)
	}
	link := w.Origin + "/reset?" + url.Values{"token": {token}}.Encode()
	if err := s.Mail.Send(ctx, account.Email, "Reset your Astilba password", "Open this one-use link to reset your password within 30 minutes:\n\n"+link); err != nil {
		w.Log.Warn("password reset mail could not be sent", "job", job.ID, "attempt", job.Attempt, diagnostic.Failure(err))
		// An unconfirmed message may arrive, and the budget has to bound what arrives.
		if !errors.Is(err, mailer.ErrUnconfirmed) {
			w.refund(ctx, job, scope)
		}
		return err
	}
	if err := s.saveResetLink(ctx, account, token); err != nil {
		w.Log.Error("password reset link could not be saved after sending", "job", job.ID, "attempt", job.Attempt, diagnostic.Failure(err))
		return err
	}
	return nil
}

func (w *ResetMailWorker) unprepared(job *river.Job[ResetMailArgs], err error) error {
	w.Log.Warn("password reset mail could not be prepared", "job", job.ID, "attempt", job.Attempt, diagnostic.Failure(err))
	return err
}

func (w *ResetMailWorker) refund(ctx context.Context, job *river.Job[ResetMailArgs], scope string) {
	undo, cancel := Detached(ctx)
	defer cancel()
	if err := w.Accounts.Store.Queries.RefundAuthAttempt(undo, Hash(scope)); err != nil {
		w.Log.Error("password reset charge could not be refunded", "job", job.ID, diagnostic.Failure(err))
	}
}

// saveResetLink replaces the account's link only now that the relay has the new one, so a failed
// send leaves the earlier link working. A credential change during the send ends this link.
func (s *Service) saveResetLink(ctx context.Context, account db.Account, token string) error {
	ctx, cancel := Detached(ctx)
	defer cancel()
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		locked, err := q.LockAccount(ctx, account.ID)
		if err != nil {
			return err
		}
		if locked.AuthVersion != account.AuthVersion {
			return nil
		}
		return q.InsertPasswordReset(ctx, db.InsertPasswordResetParams{AccountID: account.ID, TokenHash: Hash(token), ExpiresAt: s.Now().Add(resetLinkLifetime)})
	})
}
