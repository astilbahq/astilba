package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	Pool              *pgxpool.Pool
	Queries           *db.Queries
	Keys              *cryptobox.Keyring
	Hosted            bool
	Hosting           hosting.Config
	LogRetention      time.Duration
	ApplicationDomain string
	writeTurns        sync.Map
}
type Principal struct {
	Account db.Account
	MFA     bool
	// RecentUntil is when this session's last proof of its credentials stops counting.
	RecentUntil time.Time
	// CredentialRecentUntil is when it stops counting for the changes that need the shorter
	// window (WorkspaceAccess.Credential).
	CredentialRecentUntil time.Time
	// SessionHash is the stored token of the browser session, which a logout ends without
	// changing the account.
	SessionHash string
	Automation  *domain.AutomationGrant
}

// Recent is decided when it is asked, because a request can wait for the workspace lock
// for longer than its authentication stays recent.
func (p Principal) Recent() bool { return time.Now().Before(p.RecentUntil) }

// CredentialRecent is Recent for the changes that need the shorter window.
func (p Principal) CredentialRecent() bool { return time.Now().Before(p.CredentialRecentUntil) }

type Workspace struct {
	ID         uuid.UUID
	Name, Role string
	RequireMFA bool
}

// WorkspaceAccess controls admission and locking, not SQL read-only mode.
type WorkspaceAccess struct {
	Write, Owner, Recent bool
	// Credential needs the shorter recent window, for a change that lets a session holder keep
	// or widen access beyond the session, or take the workspace or its secrets elsewhere. It
	// implies Recent.
	Credential bool
	// Lock takes the workspace lock without the write requirements, for an operation that
	// only reduces access and so must not be harder than granting it.
	Lock bool
}

func New(pool *pgxpool.Pool, keys *cryptobox.Keyring, hosted bool) *Store {
	return &Store{Pool: pool, Queries: db.New(pool), Keys: keys, Hosted: hosted, LogRetention: 30 * 24 * time.Hour}
}

func Normalize(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound
	}
	var pgerr *pgconn.PgError
	// 40001 is a repeatable read finding a row changed since its snapshot, which a retry
	// resolves. Only such a transaction can raise it here.
	if errors.As(err, &pgerr) && (pgerr.Code == "23505" || pgerr.Code == "40001") {
		return domain.Conflict
	}
	return err
}

// IfMissing returns missing when err reports no matching row and err otherwise, so that a
// failed read is not answered as a refusal.
func IfMissing(err, missing error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return missing
	}
	return err
}

func (s *Store) Transaction(ctx context.Context, fn func(*db.Queries) error) error {
	return s.TransactionTx(ctx, func(_ pgx.Tx, q *db.Queries) error { return fn(q) })
}

// TransactionTx is Transaction for work that also needs the transaction itself, such as a job
// queued to commit or roll back with it.
func (s *Store) TransactionTx(ctx context.Context, fn func(pgx.Tx, *db.Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = fn(tx, db.New(tx)); err != nil {
		return Normalize(err)
	}
	return Normalize(tx.Commit(ctx))
}

// Locking the workspace serializes membership decisions and its bounded mutations.
func (s *Store) InWorkspace(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, fn func(*db.Queries, Workspace) error) error {
	return s.InWorkspaceTx(ctx, p, id, access, func(tx pgx.Tx, q *db.Queries, w Workspace) error { return fn(q, w) })
}

func (s *Store) InWorkspaceTx(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceTx(ctx, p, id, access, "management", fn)
}

func (s *Store) inWorkspaceTx(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, mode string, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceIsolation(ctx, p, id, access, mode, pgx.ReadCommitted, fn)
}

// InWorkspaceSnapshot holds a consistent view across all exported tables while
// serializing permission changes and interactive workspace mutations.
func (s *Store) InWorkspaceSnapshot(ctx context.Context, p Principal, id uuid.UUID, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	return s.inWorkspaceIsolation(ctx, p, id, WorkspaceAccess{Write: true, Owner: true, Credential: true}, "recovery", pgx.RepeatableRead, fn)
}
func (s *Store) inWorkspaceIsolation(ctx context.Context, p Principal, id uuid.UUID, access WorkspaceAccess, mode string, isolation pgx.TxIsoLevel, fn func(pgx.Tx, *db.Queries, Workspace) error) error {
	var tx pgx.Tx
	var err error
	if access.Write || access.Lock {
		// An outsider must neither wait for another tenant's lock nor take it. This
		// read admits nothing: only the read after the lock sees a revocation it waited behind.
		if _, err = s.Queries.Workspace(ctx, db.WorkspaceParams{ID: id, AccountID: p.Account.ID}); err != nil {
			return Normalize(err)
		}
		var done func()
		if tx, done, err = s.beginWorkspaceWrite(ctx, id, isolation); err != nil {
			return Normalize(err)
		}
		defer done()
	} else {
		if tx, err = s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation}); err != nil {
			return err
		}
		defer tx.Rollback(ctx)
	}
	q := db.New(tx)
	err = func() error {
		var w Workspace
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
		mfa := p.MFA
		if p.Automation != nil {
			if access.Owner || access.Recent || access.Credential {
				return domain.Forbidden
			}
			var e error
			mfa, e = s.automationAdmission(ctx, tx, p, id)
			if e != nil {
				return e
			}
		} else if access.Write || access.Lock {
			if e := browserAdmission(ctx, tx, p, isolation); e != nil {
				return e
			}
		}

		if (access.Owner && w.Role != "owner") || (access.Write && !domain.CanWrite(w.Role)) {
			return domain.Forbidden
		}
		if access.Write && (s.Hosted || w.RequireMFA) && !mfa {
			return domain.MFARequired
		}
		if access.Write && s.Hosted {
			if p.Account.EmailVerifiedAt == nil {
				return EmailVerificationRequired
			}
			entitlement, e := Entitlement(ctx, tx, id)
			if e != nil {
				return e
			}
			if mode == "management" && !entitlement.Active(time.Now()) || mode == "recovery" && !entitlement.Recoverable(time.Now()) {
				return SubscriptionRequired
			}
		}
		if access.Recent && !p.Recent() || access.Credential && !p.CredentialRecent() {
			return domain.RecentRequired
		}
		return fn(tx, q, w)
	}()
	if err != nil {
		return Normalize(err)
	}
	return Normalize(tx.Commit(ctx))
}

// AdmitBrowserSession revalidates a session in a transaction holding the lock its request
// waited for, so that a credential change or logout committed during the wait, or the
// session's expiry, wins. It covers MFA too: every change to it advances the account's
// version. A refusal rolls back and leaves no audit record, as a refused automation
// credential does. It takes no locks, so it cannot deadlock or make a revocation wait.
func AdmitBrowserSession(ctx context.Context, q *db.Queries, p Principal) error {
	_, err := q.BrowserSessionAdmission(ctx, db.BrowserSessionAdmissionParams{Token: p.SessionHash, AccountID: p.Account.ID, AuthVersion: p.Account.AuthVersion})
	return IfMissing(err, domain.Unauthenticated)
}

func browserAdmission(ctx context.Context, tx pgx.Tx, p Principal, isolation pgx.TxIsoLevel) error {
	if isolation != pgx.RepeatableRead {
		return AdmitBrowserSession(ctx, db.New(tx), p)
	}
	// The snapshot predates the wait for the lock, so a plain read would miss a revocation
	// committed during it. Locking the rows raises a serialization failure instead, which can
	// also mean an unrelated change to the account, so Normalize answers with a conflict and
	// the retry's identity check decides. The savepoint drops those locks, so this check does
	// not make a revocation wait for the export's capture.
	//
	// This read cannot deadlock. It can wait while holding the workspace lock for a holder of
	// those rows, such as a credential change or an import, but no holder goes on to wait for
	// a workspace: a workspace lock is only ever taken with no transaction open. A write's
	// audit insert already waits on the account row in the same order.
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer savepoint.Rollback(ctx)
	_, err = db.New(savepoint).BrowserSessionSnapshotAdmission(ctx, db.BrowserSessionSnapshotAdmissionParams{Token: p.SessionHash, AccountID: p.Account.ID, AuthVersion: p.Account.AuthVersion})
	return IfMissing(err, domain.Unauthenticated)
}

func Audit(ctx context.Context, q *db.Queries, p Principal, w uuid.UUID, action string, resource uuid.UUID) error {
	var credential *uuid.UUID
	if p.Automation != nil {
		credential = &p.Automation.ID
	}
	return q.InsertAudit(ctx, db.InsertAuditParams{WorkspaceID: w, ID: uuid.New(), ActorID: p.Account.ID, Action: action, ResourceID: resource, AutomationCredentialID: credential})
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
			if err := q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: w, ServerLimit: s.Hosting.ServerLimit}); err != nil {
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

// rewrapFailed names the row, because rotation stops until an operator deals with it and
// the keyring's own error is deliberately the same for every cause.
func rewrapFailed(table string, workspace, id uuid.UUID, err error) error {
	return fmt.Errorf("cannot rewrap %s row %s in workspace %s: %w", table, id, workspace, err)
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
				return rewrapFailed("secret_versions", v.WorkspaceID, v.ID, err)
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
				return rewrapFailed("git_connections", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapGitAccess(ctx, db.RewrapGitAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		authorizations, err := q.AllGitHubAuthorizations(ctx)
		if err != nil {
			return err
		}
		for _, v := range authorizations {
			blob, err := s.Keys.Rewrap(v.EncryptedAccess, pipeline.EncryptionIdentity(v.WorkspaceID, v.ID, "github-authorization"))
			if err != nil {
				return rewrapFailed("github_app_authorizations", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapGitHubAuthorization(ctx, db.RewrapGitHubAuthorizationParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
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
				return rewrapFailed("registries", v.WorkspaceID, v.ID, err)
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
				return rewrapFailed("build_secret_versions", v.WorkspaceID, v.ID, err)
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
				return rewrapFailed("backup_destinations", v.WorkspaceID, v.ID, err)
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
				return rewrapFailed("backup_policies", v.WorkspaceID, v.ID, err)
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
				return rewrapFailed("alert_settings", v.WorkspaceID, v.WorkspaceID, err)
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
					return rewrapFailed("accounts", uuid.Nil, a.ID, err)
				}
			}
			if pending != nil {
				pending, err = s.Keys.Rewrap(pending, TOTPIdentity(a.ID, true))
				if err != nil {
					return rewrapFailed("accounts", uuid.Nil, a.ID, err)
				}
			}
			if err = q.RewrapTOTP(ctx, db.RewrapTOTPParams{ID: a.ID, TotpSecret: active, TotpPending: pending}); err != nil {
				return err
			}
		}
		return nil
	})
}

const (
	// recentAuthentication is how long a confirmation of identity lets a session make
	// security-sensitive changes: long enough to prepare, review and apply a deployment plan
	// on one confirmation. It runs from the confirmation and is not extended by use.
	recentAuthentication = 15 * time.Minute
	// recentCredentialAuthentication is the shorter window for changes that let whoever holds
	// the session keep or widen access beyond it, or take the workspace or its secrets
	// elsewhere: account credentials, access grants, the workspace's MFA requirement,
	// transfer and encrypted export. A session left unattended after a confirmation for an
	// ordinary change gives less time for those.
	recentCredentialAuthentication = 5 * time.Minute
	// pendingSignIn is how long a sign-in whose password was accepted waits for its second
	// factor.
	pendingSignIn = 5 * time.Minute
	// recentAuthenticationSkew is how far ahead of the reading clock a proof's recorded
	// time may be and still count. A session records the wall-clock time, which loses
	// Go's monotonic reading, so a small backwards step of the clock, or a second process
	// whose clock is slightly ahead, can put a proof made moments ago in the future. A
	// time further ahead than this is not trusted, and does not count as recent.
	recentAuthenticationSkew = 5 * time.Second
)

func within(at, now time.Time, window time.Duration) bool {
	return !at.IsZero() && !at.After(now.Add(recentAuthenticationSkew)) && now.Sub(at) <= window
}

// Recent reports whether a proof of credentials recorded at the given time still counts.
func Recent(at time.Time, now time.Time) bool { return within(at, now, recentAuthentication) }

// PendingSignIn reports whether a sign-in whose password was accepted at the given time may
// still complete with its second factor.
func PendingSignIn(at time.Time, now time.Time) bool { return within(at, now, pendingSignIn) }

func until(at, now time.Time, window time.Duration) time.Time {
	if !within(at, now, window) {
		return time.Time{}
	}
	// A time ahead of now is taken as now, so the skew allowed never lengthens the window.
	if at.After(now) {
		at = now
	}
	return at.Add(window)
}

// RecentUntil is Principal.RecentUntil for a session that last proved its credentials at
// the given time.
func RecentUntil(at time.Time, now time.Time) time.Time {
	return until(at, now, recentAuthentication)
}

// CredentialRecentUntil is Principal.CredentialRecentUntil for a session that last proved
// its credentials at the given time.
func CredentialRecentUntil(at time.Time, now time.Time) time.Time {
	return until(at, now, recentCredentialAuthentication)
}
