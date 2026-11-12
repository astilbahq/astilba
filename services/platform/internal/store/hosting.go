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
	row, err := db.New(tx).HostedEntitlement(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return hosting.Entitlement{}, nil
	}
	if err != nil {
		return hosting.Entitlement{}, err
	}
	return hosting.Entitlement{Status: row.Status, Until: row.ManagementUntil, ServerLimit: int(row.ServerLimit)}, nil
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

// admission is what a hosted workspace's entitlement must allow for a write to be admitted.
type admission int

const (
	// admitManagement needs an active entitlement.
	admitManagement admission = iota + 1
	// admitRecovery needs an entitlement that ended no more than GracePeriod ago.
	admitRecovery
	// admitBilling and admitRevocation need none: paying must stay possible, and so must taking
	// access away, however long ago the entitlement ended or if there never was one.
	admitBilling
	admitRevocation
)

// admit refuses unless the workspace's entitlement allows mode. An unknown mode is refused, so a
// new caller can't skip the check by mistake.
func (mode admission) admit(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	switch mode {
	case admitBilling, admitRevocation:
		return nil
	case admitManagement, admitRecovery:
	default:
		return errors.New("unknown workspace admission")
	}
	e, err := Entitlement(ctx, tx, ws)
	if err != nil {
		return err
	}
	now := time.Now()
	if mode == admitManagement && !e.Active(now) || mode == admitRecovery && !e.Recoverable(now) {
		return SubscriptionRequired
	}
	return nil
}

// InWorkspaceRecovery retains normal ownership, MFA and recent-auth checks but
// permits billing repair, cancellation and portability during the paid grace.
func (s *Store) InWorkspaceRecovery(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	access.Write = true
	return s.inWorkspaceTx(ctx, p, id, access, admitRecovery, fn)
}

// InWorkspaceRevocation admits an action that only takes access away: removing a member, revoking
// a credential, invitation or server, or narrowing a policy. It keeps every check a write has
// (role, session, MFA, verified email, recent authentication) but none on the entitlement, so an
// owner can shut access off however long ago the subscription lapsed.
//
// fn must grant nothing. It may cancel or settle work, and anything it enqueues still meets
// execution's command admission at dispatch. An endpoint that may also widen access decides under
// the lock whether this request only narrows, and calls ManagementAdmission when it doesn't.
func (s *Store) InWorkspaceRevocation(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	access.Write = true
	return s.inWorkspaceTx(ctx, p, id, access, admitRevocation, fn)
}
func (s *Store) GrantManagement(ctx context.Context, ws uuid.UUID, until time.Time, limit int) error {
	if !s.Hosted || limit < 1 || limit > 100 || !until.After(time.Now()) {
		return domain.Invalid("grant")
	}
	tx, done, e := s.BeginWorkspaceWrite(ctx, ws)
	if e != nil {
		return e
	}
	defer done()
	q := db.New(tx)
	n, e := q.GrantHostedManagement(ctx, db.GrantHostedManagementParams{WorkspaceID: ws, ManagementUntil: &until, ServerLimit: int32(limit)})
	if e != nil {
		return e
	}
	if n != 1 {
		return domain.Conflict
	}
	return tx.Commit(ctx)
}

// Billing remains accessible for payment repair even after the recovery window.
func (s *Store) InWorkspaceBilling(ctx context.Context, p Principal, id uuid.UUID, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceTx(ctx, p, id, WorkspaceAccess{Write: true, Owner: true, Recent: true}, admitBilling, fn)
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
	count, e := db.New(tx).CountManagedServers(ctx, db.CountManagedServersParams{WorkspaceID: ws, ExcludeID: exclude})
	if e != nil {
		return e
	}
	if count >= int64(entitlement.ServerLimit) {
		return domain.Conflict
	}
	return nil
}

// ConsumeWorkspaceInvitation is shared by signup and workspace import. Its
// transaction owns both the cohort slot and the destination workspace creation.
func (s *Store) ConsumeWorkspaceInvitation(ctx context.Context, tx pgx.Tx, token string, now time.Time) (string, error) {
	q := db.New(tx)
	if e := q.LockHostedCohort(ctx); e != nil {
		return "", e
	}
	digest := sha256.Sum256([]byte(token))
	invitation, e := q.LockHostedInvitation(ctx, db.LockHostedInvitationParams{TokenHash: digest[:], ValidAt: now})
	if errors.Is(e, pgx.ErrNoRows) {
		return "", domain.Credentials
	}
	if e != nil {
		return "", e
	}
	count, e := q.CountWorkspaces(ctx)
	if e != nil {
		return "", e
	}
	if count >= int64(s.Hosting.CohortLimit) {
		return "", domain.Conflict
	}
	if e = q.ClaimHostedInvitation(ctx, db.ClaimHostedInvitationParams{ID: invitation.ID, ClaimedAt: &now}); e != nil {
		return "", e
	}
	return invitation.Email, nil
}
