package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const automationPrefix = "astilba_auto_"

type AutomationCredential struct {
	ID           uuid.UUID  `json:"id"`
	CreatorID    uuid.UUID  `json:"creatorId"`
	Name         string     `json:"name"`
	Capabilities []string   `json:"capabilities"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	RevokedAt    *time.Time `json:"revokedAt"`
}

func (s *Store) CreateAutomation(ctx context.Context, p Principal, ws, project, env uuid.UUID, name string, capabilities []string, expires time.Time) (AutomationCredential, string, error) {
	var out AutomationCredential
	name, err := domain.Name(name)
	if err != nil {
		return out, "", err
	}
	now := time.Now().UTC()
	if err = domain.ValidateAutomation(capabilities, expires, now); err != nil {
		return out, "", err
	}
	secret, err := randtoken.New()
	if err != nil {
		return out, "", err
	}
	out = AutomationCredential{ID: uuid.New(), CreatorID: p.Account.ID, Name: name, Capabilities: capabilities, CreatedAt: now, ExpiresAt: expires}
	hash := sha256.Sum256([]byte(secret))
	err = s.InWorkspace(ctx, p, ws, WorkspaceAccess{Write: true, Owner: true, Credential: true}, func(q *db.Queries, _ Workspace) error {
		v, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: project, ID: env})
		if err != nil {
			return err
		}
		if v.Purpose != "standard" {
			for _, cap := range capabilities {
				if cap != domain.EnvironmentRead {
					return domain.Invalid("preview.readOnlyAutomation")
				}
			}
		}
		active, err := q.CountActiveAutomationCredentials(ctx, ws)
		if err != nil {
			return err
		}
		if active >= 100 {
			return domain.Invalid("activeCredentialLimit")
		}
		err = q.InsertAutomationCredential(ctx, db.InsertAutomationCredentialParams{
			ID: out.ID, WorkspaceID: ws, EnvironmentID: env, CreatorID: p.Account.ID, Name: name, SecretHash: hash[:],
			AuthVersion: p.Account.AuthVersion, MfaApproved: p.MFA, Capabilities: capabilities, CreatedAt: now, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		return Audit(ctx, q, p, ws, "automation.create", out.ID)
	})
	if err != nil {
		return AutomationCredential{}, "", err
	}
	return out, automationPrefix + out.ID.String() + "." + secret, nil
}

func (s *Store) ListAutomation(ctx context.Context, p Principal, ws, project, env uuid.UUID) ([]AutomationCredential, error) {
	out := []AutomationCredential{}
	err := s.InWorkspace(ctx, p, ws, WorkspaceAccess{Owner: true}, func(q *db.Queries, _ Workspace) error {
		if _, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: project, ID: env}); err != nil {
			return err
		}
		rows, err := q.ListAutomationCredentials(ctx, db.ListAutomationCredentialsParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		for _, v := range rows {
			out = append(out, AutomationCredential{ID: v.ID, CreatorID: v.CreatorID, Name: v.Name, Capabilities: v.Capabilities, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt, RevokedAt: v.RevokedAt})
		}
		return nil
	})
	return out, err
}

func (s *Store) RevokeAutomation(ctx context.Context, p Principal, ws, project, env, id uuid.UUID) error {
	return s.InWorkspaceRevocation(ctx, p, ws, WorkspaceAccess{Owner: true, Recent: true}, func(_ pgx.Tx, q *db.Queries, _ Workspace) error {
		if _, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: project, ID: env}); err != nil {
			return err
		}
		n, err := q.RevokeAutomationCredential(ctx, db.RevokeAutomationCredentialParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return err
		}
		if n != 1 {
			return domain.NotFound
		}
		return Audit(ctx, q, p, ws, "automation.revoke", id)
	})
}

func (s *Store) AuthenticateAutomation(ctx context.Context, token string) (Principal, error) {
	var p Principal
	value, ok := strings.CutPrefix(token, automationPrefix)
	if !ok {
		return p, domain.Unauthenticated
	}
	identity, secret, ok := strings.Cut(value, ".")
	id, err := uuid.Parse(identity)
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(secret)
	if !ok || err != nil || len(secret) != 43 || decodeErr != nil || len(decoded) != 32 {
		return p, domain.Unauthenticated
	}
	row, err := s.Queries.AuthenticateAutomationCredential(ctx, id)
	if err != nil {
		return p, IfMissing(err, domain.Unauthenticated)
	}
	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(row.SecretHash, hash[:]) != 1 {
		return p, domain.Unauthenticated
	}
	g := domain.AutomationGrant{ID: id, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, EnvironmentID: row.EnvironmentID, Capabilities: row.Capabilities, ExpiresAt: row.ExpiresAt}
	p.Account, err = s.Queries.AccountByID(ctx, row.CreatorID)
	if err != nil {
		return Principal{}, IfMissing(err, domain.Unauthenticated)
	}
	p.Automation = &g
	return p, nil
}

// Revalidate under the workspace write lock: a revocation that committed while
// this request waited must win before any new work is admitted.
func (s *Store) automationAdmission(ctx context.Context, tx pgx.Tx, p Principal, ws uuid.UUID) (bool, error) {
	g := p.Automation
	if g.WorkspaceID != ws {
		return false, domain.Forbidden
	}
	mfa, err := db.New(tx).AutomationCredentialAdmission(ctx, db.AutomationCredentialAdmissionParams{ID: g.ID, WorkspaceID: ws, EnvironmentID: g.EnvironmentID, CreatorID: p.Account.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.Unauthenticated
	}
	return mfa, err
}
