package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// A minted key reaches the agent as an owner-supplied one does: as the destination's access.
type mintedAccess struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// minted is a key AWS made and the probe found working, not yet stored.
type minted struct {
	key  uuid.UUID
	cred awskeys.Credential
}

func (m *minted) clear() { m.cred = awskeys.Credential{} }

// mintKey runs a minting key's provider step with no locks held: it opens the account's admin
// credential, creates the key's IAM user, policy and access key, and probes the key inside its
// scope until it works. Every call is bounded by the key's mint deadline, so no create call starts
// after it; the sweep leaves a latched key alone until five minutes past that, so it revokes
// whatever a late call made. The caller activates the key, or latches it on any error.
func (s *Service) mintKey(ctx context.Context, principal secretbroker.Principal, ws, key uuid.UUID) (minted, error) {
	q := s.Store.Queries
	row, err := q.BackupStorageKey(ctx, db.BackupStorageKeyParams{WorkspaceID: ws, ID: key})
	if err != nil {
		return minted{}, err
	}
	if row.State != "minting" || row.DestinationID == nil || row.HolderServerID == nil || !row.Scope.Valid {
		return minted{}, errKeyNotMinting
	}
	account, err := q.BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: row.AccountID})
	if err != nil {
		return minted{}, err
	}
	raw, err := q.BackupDestinationConfiguration(ctx, db.BackupDestinationConfigurationParams{WorkspaceID: ws, ID: *row.DestinationID})
	if err != nil {
		return minted{}, err
	}
	var d backup.Destination
	if err = json.Unmarshal(raw, &d); err != nil {
		return minted{}, err
	}
	installation, err := q.InstallationID(ctx)
	if err != nil {
		return minted{}, err
	}
	admin, err := s.adminCredential(ctx, principal, ws, account.ID, account.EncryptedCredential, "mint")
	if err != nil {
		return minted{}, err
	}
	defer clearCredential(&admin)
	work, cancel := context.WithDeadline(context.WithoutCancel(ctx), row.MintDeadline)
	defer cancel()
	spec := awskeys.Spec{User: row.UserName, Path: awskeys.Path(ws.String()), Boundary: account.Boundary, Bucket: d.Bucket, Region: d.Region, Scope: row.Scope.String,
		Tags: map[string]string{"astilba:installation": installation.String(), "astilba:workspace": ws.String(), "astilba:server": row.HolderServerID.String(), "astilba:key": key.String()}}
	cred, err := s.AWS.Mint(work, admin, spec)
	if err != nil {
		return minted{}, err
	}
	if err = s.AWS.Probe(work, cred, spec); err != nil {
		return minted{}, err
	}
	return minted{key: key, cred: cred}, nil
}

var errKeyNotMinting = errors.New("the key is no longer minting")

// adminCredential opens a storage account's admin credential for one operation.
func (s *Service) adminCredential(ctx context.Context, principal secretbroker.Principal, ws, account uuid.UUID, cipher []byte, reason string) (awskeys.Credential, error) {
	item := secretbroker.Item{Kind: secretbroker.BackupStorageAccount, ID: account}
	session, err := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Operate, Principal: principal, Items: []secretbroker.Item{item}, Reason: reason})
	if err != nil {
		return awskeys.Credential{}, err
	}
	var cred awskeys.Credential
	err = openIntegration(session, item, cipher, &cred)
	session.Close(ctx, secretbroker.UsedIf(err == nil))
	return cred, err
}

// activateKey stores a minted key's secret and makes it active, in tx, which holds the workspace's
// turn. It reports false, storing nothing, when the key was latched meanwhile or its holder
// revoked: the sweep then revokes what the provider step made.
func (s *Service) activateKey(ctx context.Context, tx pgx.Tx, principal secretbroker.Principal, ws uuid.UUID, m minted, installs []keyInstall, recovery *uuid.UUID) (bool, error) {
	item := secretbroker.Item{Kind: secretbroker.BackupStorageKey, ID: m.key}
	session, err := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Operate, Principal: principal, Items: []secretbroker.Item{item}, Reason: "mint"})
	if err != nil {
		return false, err
	}
	stored := false
	defer func() { session.Close(ctx, secretbroker.UsedIf(stored)) }()
	secret, err := sealIntegration(ctx, db.New(tx), session, item, mintedAccess{AccessKey: m.cred.AccessKeyID, SecretKey: m.cred.SecretAccessKey})
	if err != nil {
		return false, err
	}
	if installs == nil {
		installs = []keyInstall{}
	}
	rawInstalls, err := stablejson.Marshal(installs)
	if err != nil {
		return false, err
	}
	n, err := db.New(tx).ActivateBackupStorageKey(ctx, db.ActivateBackupStorageKeyParams{WorkspaceID: ws, ID: m.key, AccessKeyID: fixedText(m.cred.AccessKeyID), EncryptedSecret: secret, Installs: rawInstalls, RecoveryID: recovery})
	stored = err == nil && n == 1
	return stored, err
}

// keyInstall is a policy generation whose install carries a key.
type keyInstall struct {
	Policy     uuid.UUID `json:"policy"`
	Generation int64     `json:"generation"`
}

// latchKey moves a key to revoking in its own transaction, for a mint that failed or was refused,
// and has the sweep run.
func (s *Service) latchKey(ctx context.Context, ws, key uuid.UUID, cause error) error {
	code := errorCode(cause)
	return s.Store.WorkspaceWriteTransaction(context.WithoutCancel(ctx), ws, func(tx pgx.Tx) error {
		if _, err := db.New(tx).LatchBackupStorageKey(ctx, db.LatchBackupStorageKeyParams{WorkspaceID: ws, ID: key, LastError: fixedText(code)}); err != nil {
			return err
		}
		return s.queueKeySweep(ctx, tx)
	})
}

type keySweepArgs struct{}

func (keySweepArgs) Kind() string { return "platform_backup_key_sweep_v1" }

var keySweepNow = river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateScheduled, rivertype.JobStateRetryable}}}

// queueKeySweep has the key sweep run now, in tx, rather than at its next minute. A key latched
// while minting waits for its deadline to pass anyway, which a later pass picks up.
func (s *Service) queueKeySweep(ctx context.Context, tx pgx.Tx) error {
	if s.Queue == nil {
		return nil
	}
	_, err := s.Queue.InsertTx(ctx, tx, keySweepArgs{}, &keySweepNow)
	return err
}

// fixedText is a stored text value that is present.
func fixedText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// textPointer is a stored text value for a view, nil when absent.
func textPointer(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// keyUserName is a minted key's IAM user name: its key ID, so a name is never reused.
func keyUserName(key uuid.UUID) string { return "astilba-" + key.String() }

// mintDeadline is how long a mint may take, matching the database's default.
const mintDeadline = 5 * time.Minute
