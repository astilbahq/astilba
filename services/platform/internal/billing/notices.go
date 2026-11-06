package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/sweep"
	"github.com/google/uuid"
)

// NotifyExpiring retries unsent mail on the next sweep. A process crash after
// SMTP accepts a message can duplicate a notice, but cannot lose its retry.
func (s *Service) NotifyExpiring(ctx context.Context, mail *mailer.Mailer) error {
	if !s.Store.Hosted {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	started, e := s.Store.Queries.SweepPassStarted(ctx)
	if e != nil {
		return e
	}
	for range 20 {
		id, e := s.Store.Queries.ClaimExpiryNotice(ctx, db.ClaimExpiryNoticeParams{ExpiringBy: s.Now().Add(24 * time.Hour), PassStarted: started})
		if errors.Is(e, pgx.ErrNoRows) || ctx.Err() != nil {
			return nil
		}
		if e != nil {
			return e
		}
		_ = sweep.Item(ctx, "expiry_notice", id, 30*time.Second, func(item context.Context) error { return s.notifyWorkspace(item, mail, id) })
	}
	return nil
}
func (s *Service) notifyWorkspace(ctx context.Context, mail *mailer.Mailer, id uuid.UUID) error {
	tx, done, e := s.Store.BeginWorkspaceWrite(ctx, id)
	if e != nil {
		return e
	}
	defer done()
	var name string
	if e = tx.QueryRow(ctx, "SELECT name FROM workspaces WHERE id=$1", id).Scan(&name); e != nil {
		return e
	}
	var until time.Time
	e = tx.QueryRow(ctx, `SELECT management_until FROM hosted_subscriptions WHERE workspace_id=$1 AND management_until<=$2 AND (notified_until IS NULL OR notified_until<>management_until)`, id, s.Now().Add(24*time.Hour)).Scan(&until)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	}
	rows, e := tx.Query(ctx, `SELECT a.email FROM accounts a JOIN memberships m ON m.account_id=a.id WHERE m.workspace_id=$1 AND m.role='owner' AND a.email_verified_at IS NOT NULL`, id)
	if e != nil {
		return e
	}
	emails := []string{}
	for rows.Next() {
		var email string
		if e = rows.Scan(&email); e != nil {
			rows.Close()
			return e
		}
		emails = append(emails, email)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(emails) == 0 {
		return nil
	}
	body := fmt.Sprintf("Management access for %s expires at %s.\n\nRunning applications, volumes and installed schedules stay on your servers. Export, recovery and controller transfer are available until %s. Renew or review billing in your workspace dashboard. The beta does not automatically delete your workspace.\n\n%s", name, until.UTC().Format(time.RFC3339), until.Add(30*24*time.Hour).UTC().Format(time.RFC3339), s.Store.Hosting.Support)
	for _, email := range emails {
		if e = mail.Send(ctx, email, "Astilba workspace access expiry", body); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE hosted_subscriptions SET notified_until=$2 WHERE workspace_id=$1", id, until); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
