package auth

import (
	"context"
	"errors"
	"net/url"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
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
	token, e := randtoken.New()
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
		undo, cancel := Detached(ctx)
		defer cancel()
		_ = s.Store.Queries.DeleteUnsentHostedInvitation(undo, Hash(token))
	}
	return e
}

// AcceptHosted charges only the source's budgets. Invitations are unguessable and few, so
// nothing about them needs an installation-wide limit that invalid ones could spend.
func (s *Service) AcceptHosted(ctx context.Context, token, name, password, workspace string, from Source, existing *store.Principal) (db.Account, uuid.UUID, error) {
	var account db.Account
	var ws uuid.UUID
	if !s.Store.Hosted {
		return account, ws, domain.Forbidden
	}
	workspace, e := domain.Name(workspace)
	if e != nil {
		return account, ws, domain.Invalid("workspace")
	}
	if existing == nil {
		if name, e = domain.Name(name); e != nil {
			return account, ws, e
		}
		if e = domain.Password(password); e != nil {
			return account, ws, e
		}
	}
	budgets := sourceBudgets("hosted-signup", from, 10)
	if e = s.refuseSpent(ctx, budgets); e != nil {
		return account, ws, e
	}
	open, e := s.Store.Queries.HostedInvitationOpen(ctx, db.HostedInvitationOpenParams{TokenHash: Hash(token), ValidAt: s.Now()})
	if e != nil {
		return account, ws, e
	}
	if !open {
		return account, ws, s.refuseLink(ctx, budgets)
	}
	var passwordHash string
	if existing == nil {
		passwordHash, e = s.hashNewPassword(ctx, passwordShare{from.principal(), true}, budgets, password)
	} else {
		e = s.charge(ctx, budgets)
	}
	if e != nil {
		return account, ws, e
	}
	ws = uuid.New()
	keys, e := s.Store.Broker.Create(ctx, secretbroker.Request{Workspace: ws, Principal: secretbroker.System("hosted-signup")})
	if e != nil {
		return account, ws, e
	}
	defer keys.Close(ctx, secretbroker.Used)
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
	if e = store.CreateWorkspace(ctx, q, keys, workspace, account.ID); e != nil {
		return account, ws, e
	}
	if e = q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: ws, ServerLimit: s.Store.Hosting.ServerLimit}); e != nil {
		return account, ws, e
	}

	if e = store.Audit(ctx, q, store.Principal{Account: account}, ws, "workspace.hosted_signup", ws); e != nil {
		return account, ws, e
	}
	if existing != nil {
		// Marking the email verified waits for any holder of the account, such as a
		// credential change or an import. A logout or credential change that committed
		// meanwhile wins, and the invitation stays unused.
		if e = store.AdmitBrowserSession(ctx, q, *existing); e != nil {
			return account, ws, e
		}
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
	token, e := randtoken.New()
	if e != nil {
		return e
	}
	e = s.Store.Queries.UpsertEmailVerification(ctx, db.UpsertEmailVerificationParams{AccountID: p.Account.ID, TokenHash: Hash(token), ExpiresAt: s.Now().Add(time.Hour)})
	if e != nil {
		return e
	}
	e = s.Mail.Send(ctx, p.Account.Email, "Verify your Astilba email", "Sign in to your Astilba account and open this one-use link to verify your email within one hour:\n\n"+origin+"/verify-email?"+url.Values{"token": {token}}.Encode())
	if e != nil {
		undo, cancel := Detached(ctx)
		defer cancel()
		_ = s.Store.Queries.DeleteUnsentEmailVerification(undo, db.DeleteUnsentEmailVerificationParams{AccountID: p.Account.ID, TokenHash: Hash(token)})
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
