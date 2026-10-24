package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var SubscriptionRequired = &domain.Error{Code: "subscription_required", Message: "An active workspace subscription is required for new management operations."}
var EmailVerificationRequired = &domain.Error{Code: "email_verification_required", Message: "Verify your email before changing a hosted workspace."}

func Entitlement(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (hosting.Entitlement, error) {
	var e hosting.Entitlement
	err := tx.QueryRow(ctx, `SELECT status,management_until,server_limit FROM hosted_subscriptions WHERE workspace_id=$1`, ws).Scan(&e.Status, &e.Until, &e.ServerLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil
	}
	return e, err
}

// ManagementAdmission is also called by automatic work and execution dispatch.
// Its caller holds the workspace lock when admitting a mutation.
func (s *Store) ManagementAdmission(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	if !s.Hosted {
		return nil
	}
	e, err := Entitlement(ctx, tx, ws)
	if err != nil {
		return err
	}
	if !e.Active(time.Now()) {
		return SubscriptionRequired
	}
	return nil
}

// InWorkspaceRecovery retains normal ownership, MFA and recent-auth checks but
// permits billing repair, cancellation and portability during the paid grace.
func (s *Store) InWorkspaceRecovery(ctx context.Context, p Principal, id uuid.UUID, owner, recent bool, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceTx(ctx, p, id, true, owner, recent, "recovery", fn)
}
func (s *Store) GrantManagement(ctx context.Context, ws uuid.UUID, until time.Time, limit int) error {
	if !s.Hosted || limit < 1 || limit > 100 || !until.After(time.Now()) {
		return domain.Invalid("grant")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
		return e
	}
	n, e := tx.Exec(ctx, `INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',$2,$3) ON CONFLICT(workspace_id) DO UPDATE SET status='granted',management_until=$2,server_limit=$3,updated_at=now() WHERE hosted_subscriptions.subscription_id IS NULL`, ws, until, limit)
	if e != nil {
		return e
	}
	if n.RowsAffected() != 1 {
		return domain.Conflict
	}
	return tx.Commit(ctx)
}

// Billing remains accessible for payment repair even after the recovery window.
func (s *Store) InWorkspaceBilling(ctx context.Context, p Principal, id uuid.UUID, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceTx(ctx, p, id, true, true, true, "billing", fn)
}

// ManagedServerAdmission counts enrolled and pending non-revoked identities.
// Renewing one existing identity never consumes an additional slot.
func (s *Store) ManagedServerAdmission(ctx context.Context, tx pgx.Tx, ws, exclude uuid.UUID) error {
	if !s.Hosted {
		return nil
	}
	entitlement, e := Entitlement(ctx, tx, ws)
	if e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM servers WHERE workspace_id=$1 AND NOT revoked AND id<>$2", ws, exclude).Scan(&count); e != nil {
		return e
	}
	if count >= entitlement.ServerLimit {
		return domain.Conflict
	}
	return nil
}

// ConsumeWorkspaceInvitation is shared by signup and workspace import. Its
// transaction owns both the cohort slot and the destination workspace creation.
func (s *Store) ConsumeWorkspaceInvitation(ctx context.Context, tx pgx.Tx, token string, now time.Time) (string, error) {
	if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4281642302)"); e != nil {
		return "", e
	}
	digest := sha256.Sum256([]byte(token))
	var id uuid.UUID
	var email string
	e := tx.QueryRow(ctx, `SELECT id,email FROM hosted_invitations WHERE token_hash=$1 AND claimed_at IS NULL AND expires_at>$2 FOR UPDATE`, digest[:], now).Scan(&id, &email)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", domain.Credentials
	}
	if e != nil {
		return "", e
	}
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM workspaces").Scan(&count); e != nil {
		return "", e
	}
	if count >= s.Hosting.CohortLimit {
		return "", domain.Conflict
	}
	if _, e = tx.Exec(ctx, "UPDATE hosted_invitations SET claimed_at=$2 WHERE id=$1", id, now); e != nil {
		return "", e
	}
	return email, nil
}
