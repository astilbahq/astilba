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
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4281642302)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM hosted_invitations WHERE claimed_at IS NULL AND expires_at<=now()"); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM workspaces)+(SELECT count(*) FROM hosted_invitations WHERE claimed_at IS NULL)`).Scan(&count); e != nil {
		return e
	}
	if count >= s.Store.Hosting.CohortLimit {
		return domain.Conflict
	}
	_, e = tx.Exec(ctx, `INSERT INTO hosted_invitations(id,email,token_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.New(), email, Hash(token), s.Now().Add(48*time.Hour))
	if e != nil {
		return store.Normalize(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	e = s.Mail.Send(ctx, email, "Your Astilba beta invitation", "Open this one-use link to create your Astilba workspace within 48 hours:\n\n"+origin+"/signup?"+url.Values{"token": {token}}.Encode())
	if e != nil {
		_, _ = s.Store.Pool.Exec(ctx, "DELETE FROM hosted_invitations WHERE token_hash=$1 AND claimed_at IS NULL", Hash(token))
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
	if _, e = tx.Exec(ctx, "UPDATE accounts SET email_verified_at=$2 WHERE id=$1", account.ID, s.Now()); e != nil {
		return account, ws, e
	}
	ws = uuid.New()
	if e = q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: ws, Name: workspace}); e != nil {
		return account, ws, e
	}
	if e = q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: account.ID, Role: "owner"}); e != nil {
		return account, ws, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO hosted_subscriptions(workspace_id,server_limit) VALUES($1,$2)`, ws, s.Store.Hosting.ServerLimit); e != nil {
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
	_, e = s.Store.Pool.Exec(ctx, `INSERT INTO email_verifications(account_id,token_hash,expires_at) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET token_hash=$2,expires_at=$3`, p.Account.ID, Hash(token), s.Now().Add(time.Hour))
	if e != nil {
		return e
	}
	e = s.Mail.Send(ctx, p.Account.Email, "Verify your Astilba email", "Sign in to your Astilba account and open this one-use link to verify your email within one hour:\n\n"+origin+"/verify-email?"+url.Values{"token": {token}}.Encode())
	if e != nil {
		_, _ = s.Store.Pool.Exec(ctx, "DELETE FROM email_verifications WHERE account_id=$1 AND token_hash=$2", p.Account.ID, Hash(token))
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
	var id uuid.UUID
	e = tx.QueryRow(ctx, `DELETE FROM email_verifications WHERE account_id=$1 AND token_hash=$2 AND expires_at>$3 RETURNING account_id`, p.Account.ID, Hash(token), s.Now()).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Credentials
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE accounts SET email_verified_at=$2 WHERE id=$1", id, s.Now()); e != nil {
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
	var exists bool
	if e := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_invitations WHERE token_hash=$1 AND claimed_at IS NULL AND expires_at>$2)`, Hash(token), s.Now()).Scan(&exists); e != nil {
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
	var exists bool
	if e := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM email_verifications WHERE account_id=$1 AND token_hash=$2 AND expires_at>$3)`, p.Account.ID, Hash(token), s.Now()).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return domain.Credentials
	}
	return nil
}
