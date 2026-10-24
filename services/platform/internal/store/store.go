package store

import (
	"context"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool         *pgxpool.Pool
	Queries      *db.Queries
	Keys         *cryptobox.Keyring
	Hosted       bool
	Hosting      hosting.Config
	LogRetention time.Duration
}
type Principal struct {
	Account db.Account
	MFA     bool
	Recent  bool
}
type Workspace struct {
	ID         uuid.UUID
	Name, Role string
	RequireMFA bool
}

func New(pool *pgxpool.Pool, keys *cryptobox.Keyring, hosted bool) *Store {
	return &Store{Pool: pool, Queries: db.New(pool), Keys: keys, Hosted: hosted, LogRetention: 30 * 24 * time.Hour}
}

func Normalize(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return domain.Conflict
	}
	return err
}

func (s *Store) Transaction(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = fn(db.New(tx)); err != nil {
		return Normalize(err)
	}
	return Normalize(tx.Commit(ctx))
}

// Locking the workspace serializes membership decisions and its bounded mutations.
func (s *Store) InWorkspace(ctx context.Context, p Principal, id uuid.UUID, write, owner, recent bool, fn func(*db.Queries, Workspace) error) error {
	return s.InWorkspaceTx(ctx, p, id, write, owner, recent, func(tx pgx.Tx, q *db.Queries, w Workspace) error { return fn(q, w) })
}

func (s *Store) InWorkspaceTx(ctx context.Context, p Principal, id uuid.UUID, write, owner, recent bool, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceTx(ctx, p, id, write, owner, recent, "management", fn)
}

func (s *Store) inWorkspaceTx(ctx context.Context, p Principal, id uuid.UUID, write, owner, recent bool, access string, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceIsolation(ctx, p, id, write, owner, recent, access, pgx.ReadCommitted, fn)
}

// InWorkspaceSnapshot holds a consistent view across all exported tables while
// serializing permission changes and interactive workspace mutations.
func (s *Store) InWorkspaceSnapshot(ctx context.Context, p Principal, id uuid.UUID, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceIsolation(ctx, p, id, true, true, true, "recovery", pgx.RepeatableRead, fn)
}
func (s *Store) inWorkspaceIsolation(ctx context.Context, p Principal, id uuid.UUID, write, owner, recent bool, access string, isolation pgx.TxIsoLevel, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	err = func() error {
		var w Workspace
		if write {
			if _, err := q.LockWorkspaceByID(ctx, id); err != nil {
				return err
			}
		}
		var r db.WorkspaceRow
		if isolation == pgx.RepeatableRead {
			// Locking the membership detects revocation committed after this
			// transaction took its snapshot while waiting for the workspace lock.
			var snapshot db.WorkspaceForSnapshotRow
			snapshot, err = q.WorkspaceForSnapshot(ctx, db.WorkspaceForSnapshotParams{ID: id, AccountID: p.Account.ID})
			r = db.WorkspaceRow(snapshot)
		} else {
			r, err = q.Workspace(ctx, db.WorkspaceParams{ID: id, AccountID: p.Account.ID})
		}
		if err != nil {
			return err
		}
		w = Workspace{r.ID, r.Name, r.Role, r.RequireMfa}

		if (owner && w.Role != "owner") || (write && !domain.CanWrite(w.Role)) {
			return domain.Forbidden
		}
		if write && (s.Hosted || w.RequireMFA) && !p.MFA {
			return domain.MFARequired
		}
		if write && s.Hosted {
			if p.Account.EmailVerifiedAt == nil {
				return EmailVerificationRequired
			}
			entitlement, e := Entitlement(ctx, tx, id)
			if e != nil {
				return e
			}
			if access == "management" && !entitlement.Active(time.Now()) || access == "recovery" && !entitlement.Recoverable(time.Now()) {
				return SubscriptionRequired
			}
		}
		if recent && !p.Recent {
			return domain.RecentRequired
		}
		return fn(tx, q, w)
	}()
	if err != nil {
		return Normalize(err)
	}
	return Normalize(tx.Commit(ctx))
}

func Audit(ctx context.Context, q *db.Queries, p Principal, w uuid.UUID, action string, resource uuid.UUID) error {
	return q.InsertAudit(ctx, db.InsertAuditParams{WorkspaceID: w, ID: uuid.New(), ActorID: p.Account.ID, Action: action, ResourceID: resource})
}

func (s *Store) Bootstrap(ctx context.Context, email, name, passwordHash, workspaceName string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.Transaction(ctx, func(q *db.Queries) error {
		if err := q.LockBootstrap(ctx); err != nil {
			return err
		}
		count, err := q.AccountCount(ctx)
		if err != nil {
			return err
		}
		if count != 0 {
			return errors.New("installation already has an owner")
		}
		id = uuid.New()
		a, err := q.InsertAccount(ctx, db.InsertAccountParams{ID: id, Email: email, Name: name, PasswordHash: passwordHash})
		if err != nil {
			return err
		}
		w := uuid.New()
		if err := q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: w, Name: workspaceName}); err != nil {
			return err
		}
		if err := q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: w, AccountID: a.ID, Role: "owner"}); err != nil {
			return err
		}
		if s.Hosted {
			if err := q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: w, ServerLimit: int32(s.Hosting.ServerLimit)}); err != nil {
				return err
			}
		}
		return Audit(ctx, q, Principal{Account: a}, w, "workspace.bootstrap", w)
	})
	return id, err
}

func SecretIdentity(workspace, id uuid.UUID) string {
	return "secret:v1:" + workspace.String() + ":" + id.String()
}
func AlertWebhookIdentity(workspace uuid.UUID) string {
	return "astilba/alert-webhook/" + workspace.String()
}
func TOTPIdentity(id uuid.UUID, pending bool) string {
	p := "totp:v1:"
	if pending {
		p = "totp-pending:v1:"
	}
	return p + id.String()
}

func (s *Store) Rewrap(ctx context.Context) error {
	return s.Transaction(ctx, func(q *db.Queries) error {
		if err := q.LockBootstrap(ctx); err != nil {
			return err
		}
		versions, err := q.AllEncryptedVersions(ctx)
		if err != nil {
			return err
		}
		for _, v := range versions {
			blob, err := s.Keys.Rewrap(v.EncryptedValue, SecretIdentity(v.WorkspaceID, v.ID))
			if err != nil {
				return err
			}
			if err := q.RewrapSecretVersion(ctx, db.RewrapSecretVersionParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedValue: blob}); err != nil {
				return err
			}
		}

		git, err := q.AllGitAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range git {
			blob, err := s.Keys.Rewrap(v.EncryptedAccess, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "git"))
			if err != nil {
				return err
			}
			if err = q.RewrapGitAccess(ctx, db.RewrapGitAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		registries, err := q.AllRegistryAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range registries {
			blob, err := s.Keys.Rewrap(v.EncryptedAccess, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "registry"))
			if err != nil {
				return err
			}
			if err = q.RewrapRegistryAccess(ctx, db.RewrapRegistryAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		build, err := q.AllBuildSecrets(ctx)
		if err != nil {
			return err
		}
		for _, v := range build {
			blob, err := s.Keys.Rewrap(v.EncryptedValue, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "build-secret"))
			if err != nil {
				return err
			}
			if err = q.RewrapBuildSecret(ctx, db.RewrapBuildSecretParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedValue: blob}); err != nil {
				return err
			}
		}

		rowsBackupDestination, err := q.AllBackupDestinationAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range rowsBackupDestination {
			blob, err := s.Keys.Rewrap(v.EncryptedAccess, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "backup-destination"))
			if err != nil {
				return err
			}
			if err = q.RewrapBackupDestinationAccess(ctx, db.RewrapBackupDestinationAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}

		rowsBackupPolicy, err := q.AllBackupPolicyAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range rowsBackupPolicy {
			blob, err := s.Keys.Rewrap(v.EncryptedAccess, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "backup-policy"))
			if err != nil {
				return err
			}
			if err = q.RewrapBackupPolicyAccess(ctx, db.RewrapBackupPolicyAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		hooks, err := q.AllAlertWebhooks(ctx)
		if err != nil {
			return err
		}
		for _, v := range hooks {
			blob, err := s.Keys.Rewrap(v.EncryptedWebhook, AlertWebhookIdentity(v.WorkspaceID))
			if err != nil {
				return err
			}
			if err = q.RewrapAlertWebhook(ctx, db.RewrapAlertWebhookParams{WorkspaceID: v.WorkspaceID, EncryptedWebhook: blob}); err != nil {
				return err
			}
		}
		accounts, err := q.AllTOTPSecrets(ctx)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			active, pending := a.TotpSecret, a.TotpPending
			if active != nil {
				active, err = s.Keys.Rewrap(active, TOTPIdentity(a.ID, false))
				if err != nil {
					return err
				}
			}
			if pending != nil {
				pending, err = s.Keys.Rewrap(pending, TOTPIdentity(a.ID, true))
				if err != nil {
					return err
				}
			}
			if err = q.RewrapTOTP(ctx, db.RewrapTOTPParams{ID: a.ID, TotpSecret: active, TotpPending: pending}); err != nil {
				return err
			}
		}
		return nil
	})
}

func Recent(at time.Time, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= 5*time.Minute
}
