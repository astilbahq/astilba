package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
)

// The account check compares each storage account's users under its workspace's path with the
// keys management holds for it, once a day or when the owner asks. A restore can leave keys minted
// after its backup with no row, and a leaked admin credential can make users of its own, so a user
// with no row is reported unless it belongs to a listed peer installation, and the check deletes
// access keys management never made. See docs/backups.md#managed-keys.
type accountCheckArgs struct{}

func (accountCheckArgs) Kind() string { return "platform_backup_account_check_v1" }

type accountCheckWorker struct {
	river.WorkerDefaults[accountCheckArgs]
	service *Service
}

// A check lists a workspace's users and asks IAM about each one, which can take minutes.
func (w *accountCheckWorker) Timeout(*river.Job[accountCheckArgs]) time.Duration {
	return accountCheckPass + accountCheckLimit
}

const (
	accountCheckPass  = 5 * time.Minute // no account check starts after this
	accountCheckLimit = 3 * time.Minute // one account's check
	accountUserLimit  = 1000            // users listed per account
)

var accountCheckPrincipal = secretbroker.System("backup-account-check")

func (w *accountCheckWorker) Work(ctx context.Context, _ *river.Job[accountCheckArgs]) error {
	s := w.service
	began := time.Now()
	for time.Since(began) < accountCheckPass && ctx.Err() == nil {
		claimed, err := s.Store.Queries.ClaimDueBackupStorageAccountCheck(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if err = s.checkAccount(ctx, claimed.WorkspaceID, claimed.ID); err != nil {
			return err
		}
	}
	return nil
}

// queueAccountCheck has the account check run now rather than at its next ten minutes.
func (s *Service) queueAccountCheck(ctx context.Context, tx pgx.Tx) error {
	if s.Queue == nil {
		return nil
	}
	_, err := s.Queue.InsertTx(ctx, tx, accountCheckArgs{}, &keySweepNow)
	return err
}

// checkAccount checks one account and records what it found. A provider failure is recorded on
// the account, which raises backup_account; only a database failure is returned.
func (s *Service) checkAccount(ctx context.Context, ws, id uuid.UUID) error {
	account, err := s.Store.Queries.BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, accountCheckLimit)
	defer cancel()
	result, checkErr := s.listAccount(work, account)
	if checkErr != nil && (ctx.Err() != nil || !providerFailure(checkErr)) {
		return errors.Join(ctx.Err(), checkErr)
	}
	return s.Store.WorkspaceWriteTransaction(context.WithoutCancel(ctx), ws, func(tx pgx.Tx) error {
		q := db.New(tx)
		report := accountReport{}
		code := pgtype.Text{}
		if checkErr != nil {
			// A failed check keeps what the last one found.
			code = fixedText(errorCode(checkErr))
			if len(account.Report) > 0 {
				if err := json.Unmarshal(account.Report, &report); err != nil {
					return err
				}
			}
		} else {
			if report, err = s.settleAccount(ctx, tx, account, result); err != nil {
				return err
			}
		}
		raw, err := stablejson.Marshal(report)
		if err != nil {
			return err
		}
		return q.RecordBackupStorageAccountCheck(ctx, db.RecordBackupStorageAccountCheckParams{WorkspaceID: ws, ID: id, Generation: account.Generation, CheckError: code, Report: raw})
	})
}

// providerFailure says whether a check failed at AWS, which is recorded on the account, rather than
// in management, which the job reports.
func providerFailure(err error) bool {
	for _, e := range []error{awskeys.ErrCredential, awskeys.ErrDenied, awskeys.ErrNotFound, awskeys.ErrUnavailable, awskeys.ErrNotAdmin, awskeys.ErrConflict, errAccountMoved, context.DeadlineExceeded} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// accountListing is what AWS said about an account's users, with the keys management held once
// the listing was done: every key's row is written before its user is created, so a user created
// during the listing is never taken for one management doesn't know.
type accountListing struct {
	began     time.Time
	installed uuid.UUID
	users     []awskeys.User
	truncated bool
	keys      []db.AccountBackupStorageKeysRow
	extra     int
	missing   map[string]bool // known active users that IAM confirmed gone
}

func (s *Service) listAccount(ctx context.Context, account db.BackupStorageAccountRow) (accountListing, error) {
	ws := account.WorkspaceID
	l := accountListing{began: time.Now(), missing: map[string]bool{}}
	var err error
	if l.installed, err = s.Store.Queries.InstallationID(ctx); err != nil {
		return l, err
	}
	admin, err := s.adminCredential(ctx, accountCheckPrincipal, ws, account.ID, account.EncryptedCredential, "account-check")
	if err != nil {
		return l, err
	}
	defer clearCredential(&admin)
	who, err := s.AWS.Identify(ctx, admin)
	if err != nil {
		return l, err
	}
	// Another account's credential would find none of this account's users.
	if who.Account != account.AwsAccount {
		return l, errAccountMoved
	}
	if l.users, err = s.AWS.Users(ctx, admin, awskeys.Path(ws.String()), accountUserLimit); err != nil {
		return l, err
	}
	l.truncated = len(l.users) >= accountUserLimit
	if l.keys, err = s.Store.Queries.AccountBackupStorageKeys(ctx, db.AccountBackupStorageKeysParams{WorkspaceID: ws, AccountID: account.ID}); err != nil {
		return l, err
	}
	listed := map[string]bool{}
	for _, u := range l.users {
		listed[u.Name] = true
	}
	for _, k := range l.keys {
		if k.State != "active" || !k.AccessKeyID.Valid {
			continue
		}
		if !listed[k.UserName] {
			// A listing can lag a create, so only a key activated well before it began is
			// asked about, and only IAM's own answer for the user counts.
			if l.truncated || k.ActivatedAt == nil || k.ActivatedAt.After(l.began.Add(-time.Hour)) {
				continue
			}
			exists, err := s.AWS.UserExists(ctx, admin, k.UserName)
			if err != nil {
				return l, err
			}
			if !exists {
				l.missing[k.UserName] = true
			}
			continue
		}
		// Management mints one access key per user, so any other was never its own.
		ids, err := s.AWS.AccessKeys(ctx, admin, k.UserName)
		if err != nil {
			return l, err
		}
		for _, other := range ids {
			if other == k.AccessKeyID.String {
				continue
			}
			if err = s.AWS.DeleteAccessKey(ctx, admin, k.UserName, other); err != nil {
				return l, err
			}
			l.extra++
		}
	}
	return l, nil
}

// settleAccount records what a listing found, in the account's workspace's turn, and returns the
// report: found users, latching those this installation's revoked servers held, and known keys
// IAM no longer has or still has.
func (s *Service) settleAccount(ctx context.Context, tx pgx.Tx, account db.BackupStorageAccountRow, l accountListing) (accountReport, error) {
	ws, q := account.WorkspaceID, db.New(tx)
	report := accountReport{Found: []api.BackupStorageFoundUser{}, ExtraKeysDeleted: l.extra, Alert: l.extra > 0}
	servers, err := q.WorkspaceServerStates(ctx, ws)
	if err != nil {
		return report, err
	}
	revoked := map[string]bool{}
	for _, sv := range servers {
		revoked[sv.ID.String()] = sv.Revoked
	}
	known := map[string]db.AccountBackupStorageKeysRow{}
	for _, k := range l.keys {
		known[k.UserName] = k
	}
	for _, k := range l.keys {
		if l.missing[k.UserName] {
			if _, err := q.LatchBackupStorageKey(ctx, db.LatchBackupStorageKeyParams{WorkspaceID: ws, ID: k.ID, LastError: fixedText("user_missing")}); err != nil {
				return report, err
			}
		}
	}
	latched := false
	for _, u := range l.users {
		if k, ok := known[u.Name]; ok {
			// A user recorded revoked over an hour ago that IAM still lists is revoked again.
			if k.State == "revoked" && k.RevokedAt != nil && k.RevokedAt.Before(l.began.Add(-time.Hour)) {
				n, err := q.RelatchRevokedBackupStorageKey(ctx, db.RelatchRevokedBackupStorageKeyParams{WorkspaceID: ws, ID: k.ID})
				if err != nil {
					return report, err
				}
				latched = latched || n > 0
			}
			continue
		}
		found := api.BackupStorageFoundUser{UserName: u.Name, InstallationId: u.Tags["astilba:installation"], ServerId: u.Tags["astilba:server"], Bounded: u.Boundary == account.Boundary}
		peer, err := uuid.Parse(found.InstallationId)
		found.Peer = err == nil && slices.Contains(account.Peers, peer)
		switch {
		case !found.Bounded:
			// A user without the boundary isn't confined to backups, whatever its tags say.
			report.Alert = true
		case found.InstallationId == l.installed.String() && revoked[found.ServerId]:
			tags, err := stablejson.Marshal(map[string]string{"astilba:installation": found.InstallationId, "astilba:server": found.ServerId})
			if err != nil {
				return report, err
			}
			if err = q.InsertFoundBackupStorageKey(ctx, db.InsertFoundBackupStorageKeyParams{ID: uuid.New(), WorkspaceID: ws, AccountID: account.ID, UserName: u.Name, FoundTags: tags}); err != nil {
				return report, err
			}
			found.Revoking, latched = true, true
		case found.Peer:
		default:
			report.Alert = true
		}
		report.Found = append(report.Found, found)
	}
	if latched || len(l.missing) > 0 {
		return report, s.queueKeySweep(ctx, tx)
	}
	return report, nil
}
