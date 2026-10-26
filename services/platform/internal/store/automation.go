package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
	secret, err := cryptobox.Token()
	if err != nil {
		return out, "", err
	}
	out = AutomationCredential{ID: uuid.New(), CreatorID: p.Account.ID, Name: name, Capabilities: capabilities, CreatedAt: now, ExpiresAt: expires}
	hash := sha256.Sum256([]byte(secret))
	err = s.InWorkspaceTx(ctx, p, ws, true, true, true, func(tx pgx.Tx, q *db.Queries, _ Workspace) error {
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
		var active int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM automation_credentials WHERE workspace_id=$1 AND revoked_at IS NULL AND expires_at>now()", ws).Scan(&active); err != nil {
			return err
		}
		if active >= 100 {
			return domain.Invalid("activeCredentialLimit")
		}
		_, err = tx.Exec(ctx, `INSERT INTO automation_credentials(id,workspace_id,environment_id,creator_id,name,secret_hash,auth_version,mfa_approved,capabilities,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, out.ID, ws, env, p.Account.ID, name, hash[:], p.Account.AuthVersion, p.MFA, capabilities, now, expires)
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
	err := s.InWorkspaceTx(ctx, p, ws, false, true, false, func(tx pgx.Tx, q *db.Queries, _ Workspace) error {
		if _, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: project, ID: env}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id,creator_id,name,capabilities,created_at,expires_at,revoked_at FROM automation_credentials WHERE workspace_id=$1 AND environment_id=$2 ORDER BY (revoked_at IS NULL AND expires_at>now()) DESC,created_at DESC,id LIMIT 200", ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v AutomationCredential
			if err = rows.Scan(&v.ID, &v.CreatorID, &v.Name, &v.Capabilities, &v.CreatedAt, &v.ExpiresAt, &v.RevokedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) RevokeAutomation(ctx context.Context, p Principal, ws, project, env, id uuid.UUID) error {
	return s.InWorkspaceTx(ctx, p, ws, true, true, true, func(tx pgx.Tx, q *db.Queries, _ Workspace) error {
		if _, err := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: project, ID: env}); err != nil {
			return err
		}
		n, err := tx.Exec(ctx, "UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id)
		if err != nil {
			return err
		}
		if n.RowsAffected() != 1 {
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
	var creator uuid.UUID
	var stored []byte
	g := domain.AutomationGrant{ID: id}
	err = s.Pool.QueryRow(ctx, `SELECT c.workspace_id,e.project_id,c.environment_id,c.creator_id,c.secret_hash,c.capabilities,c.expires_at
 FROM automation_credentials c JOIN environments e ON e.workspace_id=c.workspace_id AND e.id=c.environment_id
 JOIN accounts a ON a.id=c.creator_id JOIN memberships m ON m.workspace_id=c.workspace_id AND m.account_id=c.creator_id
 WHERE c.id=$1 AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner'`, id).Scan(&g.WorkspaceID, &g.ProjectID, &g.EnvironmentID, &creator, &stored, &g.Capabilities, &g.ExpiresAt)
	hash := sha256.Sum256([]byte(secret))
	if err != nil || subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
		return p, domain.Unauthenticated
	}
	p.Account, err = s.Queries.AccountByID(ctx, creator)
	if err != nil {
		return Principal{}, domain.Unauthenticated
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
	var mfa bool
	err := tx.QueryRow(ctx, `SELECT c.mfa_approved AND a.totp_secret IS NOT NULL
 FROM automation_credentials c JOIN accounts a ON a.id=c.creator_id
 JOIN memberships m ON m.workspace_id=c.workspace_id AND m.account_id=c.creator_id
 WHERE c.id=$1 AND c.workspace_id=$2 AND c.environment_id=$3 AND c.creator_id=$4 AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.auth_version=a.auth_version AND m.role='owner'`, g.ID, ws, g.EnvironmentID, p.Account.ID).Scan(&mfa)
	if err == pgx.ErrNoRows {
		return false, domain.Unauthenticated
	}
	return mfa, err
}
