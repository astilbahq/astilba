package auth

import (
	"context"
	"errors"
	"net/url"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) InviteHosted(ctx context.Context, email, origin string) error {
	if !s.Store.Hosted {
		return domain.Forbidden
	}
	email, e := domain.Email(email)
	if e != nil {
		return e
	}
	token, e := cryptobox.Token()
	if e != nil {
		return e
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	if e = q.LockHostedCohort(ctx); e != nil {
		return e
	}
	if e = q.DeleteExpiredHostedInvitations(ctx); e != nil {
		return e
	}
	count, e := q.HostedCohortSize(ctx)
	if e != nil {
		return e
	}
	if count >= int64(s.Store.Hosting.CohortLimit) {
		return domain.Conflict
	}
	e = q.InsertHostedInvitation(ctx, db.InsertHostedInvitationParams{ID: uuid.New(), Email: email, TokenHash: Hash(token), ExpiresAt: s.Now().Add(48 * time.Hour)})
	if e != nil {
		return store.Normalize(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	e = s.Mail.Send(ctx, email, "Your Astilba beta invitation", "Open this one-use link to create your Astilba workspace within 48 hours:\n\n"+origin+"/signup?"+url.Values{"token": {token}}.Encode())
	if e != nil {
		_ = s.Store.Queries.DeleteUnsentHostedInvitation(ctx, Hash(token))
	}
	return e
}

func (s *Service) AcceptHosted(ctx context.Context, token, name, password, workspace string, existing *store.Principal) (db.Account, uuid.UUID, error) {
	var account db.Account
	var ws uuid.UUID
	if !s.Store.Hosted {
		return account, ws, domain.Forbidden
	}
	if err := s.Limit(ctx, "hosted-signup:global", 100, 15*time.Minute); err != nil {
		return account, ws, err
	}
	workspace, e := domain.Name(workspace)
	if e != nil {
		return account, ws, domain.Invalid("workspace")
	}
	var passwordHash string
	if existing == nil {
		name, e = domain.Name(name)
		if e != nil {
			return account, ws, e
		}
		passwordHash, e = s.Passwords.Hash(ctx, password)
		if e != nil {
			return account, ws, e
		}
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return account, ws, e
	}
	defer tx.Rollback(ctx)
	email, e := s.Store.ConsumeWorkspaceInvitation(ctx, tx, token, s.Now())
	if e != nil {
		return account, ws, e
	}

	q := db.New(tx)
	if existing != nil {
		if existing.Account.Email != email {
			return account, ws, domain.Forbidden
		}
		if !existing.MFA {
			return account, ws, domain.MFARequired
		}
		account = existing.Account
	} else {
		account, e = q.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: email, Name: name, PasswordHash: passwordHash})
		if e != nil {
			return account, ws, store.Normalize(e)
		}
	}
	if e = q.MarkEmailVerified(ctx, db.MarkEmailVerifiedParams{ID: account.ID, EmailVerifiedAt: ptrTime(s.Now())}); e != nil {
		return account, ws, e
	}
	ws = uuid.New()
	if e = q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: ws, Name: workspace}); e != nil {
		return account, ws, e
	}
	if e = q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: account.ID, Role: "owner"}); e != nil {
		return account, ws, e
	}
	if e = q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: ws, ServerLimit: int32(s.Store.Hosting.ServerLimit)}); e != nil {
		return account, ws, e
	}

	if e = store.Audit(ctx, q, store.Principal{Account: account}, ws, "workspace.hosted_signup", ws); e != nil {
		return account, ws, e
	}
	return account, ws, tx.Commit(ctx)
}

func (s *Service) RequestEmailVerification(ctx context.Context, p store.Principal, origin string) error {
	if p.Account.EmailVerifiedAt != nil {
		return nil
	}
	if e := s.Limit(ctx, "email-verification:"+p.Account.ID.String(), 3, time.Hour); e != nil {
		return e
	}
	token, e := cryptobox.Token()
	if e != nil {
		return e
	}
	e = s.Store.Queries.UpsertEmailVerification(ctx, db.UpsertEmailVerificationParams{AccountID: p.Account.ID, TokenHash: Hash(token), ExpiresAt: s.Now().Add(time.Hour)})
	if e != nil {
		return e
	}
	e = s.Mail.Send(ctx, p.Account.Email, "Verify your Astilba email", "Sign in to your Astilba account and open this one-use link to verify your email within one hour:\n\n"+origin+"/verify-email?"+url.Values{"token": {token}}.Encode())
	if e != nil {
		_ = s.Store.Queries.DeleteUnsentEmailVerification(ctx, db.DeleteUnsentEmailVerificationParams{AccountID: p.Account.ID, TokenHash: Hash(token)})
	}
	return e
}
func (s *Service) VerifyEmail(ctx context.Context, p store.Principal, token string) error {
	if e := s.Limit(ctx, "verify-email:"+p.Account.ID.String(), 10, time.Hour); e != nil {
		return e
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	id, e := q.ConsumeEmailVerification(ctx, db.ConsumeEmailVerificationParams{AccountID: p.Account.ID, TokenHash: Hash(token), ValidAt: s.Now()})
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Credentials
	}
	if e != nil {
		return e
	}
	if e = q.MarkEmailVerified(ctx, db.MarkEmailVerifiedParams{ID: id, EmailVerifiedAt: ptrTime(s.Now())}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) ValidateHostedInvitation(ctx context.Context, token, ip string) error {
	if !s.Store.Hosted {
		return domain.Forbidden
	}
	if e := s.Limit(ctx, "hosted-validate:ip:"+ip, 60, 15*time.Minute); e != nil {
		return e
	}
	exists, e := s.Store.Queries.HostedInvitationOpen(ctx, db.HostedInvitationOpenParams{TokenHash: Hash(token), ValidAt: s.Now()})
	if e != nil {
		return e
	}
	if !exists {
		return domain.Credentials
	}
	return nil
}

func (s *Service) ValidateEmailVerification(ctx context.Context, p store.Principal, token string) error {
	if e := s.Limit(ctx, "email-validate:"+p.Account.ID.String(), 60, 15*time.Minute); e != nil {
		return e
	}
	exists, e := s.Store.Queries.EmailVerificationOpen(ctx, db.EmailVerificationOpenParams{AccountID: p.Account.ID, TokenHash: Hash(token), ValidAt: s.Now()})
	if e != nil {
		return e
	}
	if !exists {
		return domain.Credentials
	}
	return nil
}
