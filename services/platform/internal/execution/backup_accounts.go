package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A workspace keeps few storage accounts: each is one AWS account's admin credential.
const backupAccountLimit = 10

var (
	errAccountCredential   = &domain.Error{Code: "invalid_input", Message: "AWS did not accept this access key.", Fields: []string{"credential"}}
	errAccountDenied       = &domain.Error{Code: "invalid_input", Message: "AWS refused to list users under this workspace's path with this access key. Attach the admin policy from docs/backups.md#managed-keys to its user.", Fields: []string{"credential"}}
	errAccountNotAdmin     = &domain.Error{Code: "invalid_input", Message: "The access key must belong to an IAM user in a commercial AWS region, outside the /astilba/ path: not the root user or a role.", Fields: []string{"credential"}}
	errAccountBoundary     = &domain.Error{Code: "invalid_input", Message: "The permissions boundary must be a customer-managed policy in the same AWS account.", Fields: []string{"boundary"}}
	errAccountOther        = &domain.Error{Code: "invalid_input", Message: "This access key belongs to another AWS account. A storage account's credential can only be replaced with one of the same account.", Fields: []string{"credential"}}
	errAccountTaken        = &domain.Error{Code: "conflict", Message: "This workspace already has a storage account for this AWS account.", Fields: []string{"credential"}}
	errFoundPeer           = &domain.Error{Code: "conflict", Message: "This user belongs to a peer installation, which may still use its key. Revoke it from that installation."}
	errPeerLimit           = &domain.Error{Code: "conflict", Message: "A storage account lists at most 16 peer installations."}
	errFoundUnbounded      = &domain.Error{Code: "conflict", Message: "This user has no permissions boundary, so the storage account's admin policy can't change it. Delete it in the AWS console."}
	errAcknowledgeUnbounded = &domain.Error{Code: "conflict", Message: "This user has no permissions boundary, so it isn't confined to backups, whoever made it. Delete it in the AWS console."}
	errAcknowledgeNotPeer   = &domain.Error{Code: "conflict", Message: "Only a peer installation's user can be acknowledged. Add its installation as a peer if this workspace was transferred to or from it, or revoke the user."}
	errFoundChanged         = &domain.Error{Code: "conflict", Message: "The latest check found this user with another IAM user ID or other access keys than you reviewed. Review it again."}
	errProviderUnavailable = &domain.Error{Code: "provider_unavailable", Message: "AWS could not be reached or failed. Try again shortly."}
)

// providerError is the owner's error for an AWS failure while checking or minting.
func providerError(err error) error {
	switch {
	case errors.Is(err, awskeys.ErrCredential):
		return errAccountCredential
	case errors.Is(err, awskeys.ErrDenied):
		return errAccountDenied
	case errors.Is(err, awskeys.ErrNotAdmin):
		return errAccountNotAdmin
	}
	return errProviderUnavailable
}

// errorCode is the fixed code recorded for a provider failure. A provider's own message is never
// recorded or shown.
func errorCode(err error) string {
	switch {
	case errors.Is(err, awskeys.ErrCredential):
		return "credential_refused"
	case errors.Is(err, awskeys.ErrDenied):
		return "access_denied"
	case errors.Is(err, awskeys.ErrNotFound):
		return "not_found"
	case errors.Is(err, awskeys.ErrNotAdmin):
		return "not_admin"
	case errors.Is(err, awskeys.ErrConflict):
		return "delete_conflict"
	case errors.Is(err, errKeyWorks):
		return "moved_off_path"
	case errors.Is(err, awskeys.ErrUnlisted):
		return "not_listed"
	case errors.Is(err, errAccountMoved):
		return "account_changed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "unavailable"
}

// errAccountMoved is a credential that now answers for another AWS account than the one recorded.
var errAccountMoved = errors.New("the credential belongs to another AWS account")

// checkAdmin checks an admin credential with no locks held: whose it is, that it belongs to
// account when one is given, that its boundary is that account's, and that its policy lets it
// list the workspace's path.
func (s *Service) checkAdmin(ctx context.Context, ws uuid.UUID, cred awskeys.Credential, account, boundary string) (awskeys.Identity, error) {
	if !cred.Valid() {
		return awskeys.Identity{}, errAccountCredential
	}
	id, err := s.AWS.Identify(ctx, cred)
	if err != nil {
		return id, providerError(err)
	}
	if account != "" && id.Account != account {
		return id, errAccountOther
	}
	if !awskeys.ValidBoundary(boundary, id.Account) {
		return id, errAccountBoundary
	}
	if err = s.AWS.CanList(ctx, cred, awskeys.Path(ws.String())); err != nil {
		return id, providerError(err)
	}
	return id, nil
}

func (s *Service) CreateBackupStorageAccount(ctx context.Context, p store.Principal, ws uuid.UUID, in api.BackupStorageAccountRequest) (api.BackupStorageAccount, error) {
	name, err := domain.Name(in.Name)
	if err != nil {
		return api.BackupStorageAccount{}, domain.Invalid("name")
	}
	// Only an owner who may make the change has management call AWS with what they sent.
	if err = s.Store.Admit(ctx, p, ws, ownerSettings.admission(), false); err != nil {
		return api.BackupStorageAccount{}, err
	}
	cred := awskeys.Credential{AccessKeyID: writeOnly(in.AccessKeyId), SecretAccessKey: writeOnly(in.SecretAccessKey)}
	defer clearCredential(&cred)
	id, err := s.checkAdmin(ctx, ws, cred, "", in.Boundary)
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	account := uuid.New()
	item := secretbroker.Item{Kind: secretbroker.BackupStorageAccount, ID: account}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.account.create", account, func(tx pgx.Tx) error {
		q := db.New(tx)
		rows, err := q.ListBackupStorageAccounts(ctx, ws)
		if err != nil {
			return err
		}
		if len(rows) >= backupAccountLimit {
			return domain.Conflict
		}
		// Two accounts for one AWS account would mint under one path and report each other's users.
		if slices.ContainsFunc(rows, func(r db.ListBackupStorageAccountsRow) bool { return r.AwsAccount == id.Account }) {
			return errAccountTaken
		}
		secret, err := sealIntegration(ctx, q, keys, item, cred)
		if err != nil {
			return err
		}
		sealed = true
		if err = q.InsertBackupStorageAccount(ctx, db.InsertBackupStorageAccountParams{ID: account, WorkspaceID: ws, Name: name, AwsAccount: id.Account, AdminArn: id.ARN, Boundary: in.Boundary, EncryptedCredential: secret}); err != nil {
			return err
		}
		// A new account is checked at once, so the owner sees what is already under its path.
		return s.queueAccountCheck(ctx, tx)
	})
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	return s.backupStorageAccount(ctx, p, ws, account)
}

func (s *Service) ReplaceBackupStorageAccountCredential(ctx context.Context, p store.Principal, ws, account uuid.UUID, in api.BackupStorageCredentialRequest) (api.BackupStorageAccount, error) {
	if err := s.Store.Admit(ctx, p, ws, ownerSettings.admission(), false); err != nil {
		return api.BackupStorageAccount{}, err
	}
	var recorded db.BackupStorageAccountRow
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		var err error
		recorded, err = db.New(tx).BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: account})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.BackupStorageAccount{}, domain.NotFound
	} else if err != nil {
		return api.BackupStorageAccount{}, err
	}
	cred := awskeys.Credential{AccessKeyID: writeOnly(in.AccessKeyId), SecretAccessKey: writeOnly(in.SecretAccessKey)}
	defer clearCredential(&cred)
	// Minted users are deleted by the account they live in: another account's credential would
	// report every one of them missing, and so revoked. The boundary stays the account's for its
	// life: the admin policy can change only users with the boundary it names, and every user
	// minted so far carries this one.
	id, err := s.checkAdmin(ctx, ws, cred, recorded.AwsAccount, recorded.Boundary)
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	item := secretbroker.Item{Kind: secretbroker.BackupStorageAccount, ID: account}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.account.rotate", account, func(tx pgx.Tx) error {
		q := db.New(tx)
		secret, err := sealIntegration(ctx, q, keys, item, cred)
		if err != nil {
			return err
		}
		sealed = true
		n, err := q.ReplaceBackupStorageAccountCredential(ctx, db.ReplaceBackupStorageAccountCredentialParams{WorkspaceID: ws, ID: account, Generation: in.Generation, AdminArn: id.ARN, EncryptedCredential: secret})
		if err == nil && n != 1 {
			return domain.Conflict
		}
		if err == nil {
			// A replaced credential is checked again at once, and failing revocations retry with it.
			err = q.RequestBackupStorageAccountCheck(ctx, db.RequestBackupStorageAccountCheckParams{WorkspaceID: ws, ID: account})
		}
		if err == nil {
			_, err = q.RetryAccountBackupStorageKeys(ctx, db.RetryAccountBackupStorageKeysParams{WorkspaceID: ws, AccountID: account})
		}
		if err == nil {
			err = errors.Join(s.queueAccountCheck(ctx, tx), s.queueKeySweep(ctx, tx))
		}
		return err
	})
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	return s.backupStorageAccount(ctx, p, ws, account)
}

func clearCredential(c *awskeys.Credential) { *c = awskeys.Credential{} }

func (s *Service) BackupStorageAccounts(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.BackupStorageAccount, error) {
	out := []api.BackupStorageAccount{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		q := db.New(tx)
		installation, err := q.InstallationID(ctx)
		if err != nil {
			return err
		}
		rows, err := q.ListBackupStorageAccounts(ctx, ws)
		if err != nil {
			return err
		}
		revoking, err := q.RevokingFoundBackupStorageKeys(ctx, ws)
		if err != nil {
			return err
		}
		acks, err := q.BackupStorageAcknowledgements(ctx, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			v, err := accountView(ws, installation, row, revoking, acks)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

func (s *Service) backupStorageAccount(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.BackupStorageAccount, error) {
	all, err := s.BackupStorageAccounts(ctx, p, ws)
	if err != nil {
		return api.BackupStorageAccount{}, err
	}
	for _, v := range all {
		if v.Id == id {
			return v, nil
		}
	}
	return api.BackupStorageAccount{}, domain.NotFound
}

// accountReport is what the latest account check found, as it is stored.
type accountReport struct {
	Found            []api.BackupStorageFoundUser `json:"found"`
	ExtraKeysDeleted int                          `json:"extraKeysDeleted"`
	// Alert raises backup_account: a user that isn't a peer's or isn't bounded, or a deleted key.
	Alert bool `json:"alert"`
}

// isPeer reports whether a found user's installation tag names one of the account's peers.
// Management tags users with an installation's canonical UUID text, so a tag in any other form
// isn't a peer's, as UnacknowledgedPeerBackupStorageUsers also reads it.
func isPeer(installation string, peers []uuid.UUID) bool {
	id, err := uuid.Parse(installation)
	return err == nil && id.String() == installation && slices.Contains(peers, id)
}

// foundUserID is a found user's identity under an account: its name and IAM user ID, which IAM
// gives each user it creates, so a user made again under the same name has another. An
// acknowledgement is of one identity, and a backup_peer_user alert names it.
func foundUserID(account uuid.UUID, name, iamUserID string) uuid.UUID {
	return uuid.NewSHA1(account, []byte("iam-user\x00"+name+"\x00"+iamUserID))
}

// acknowledgement is the time an acknowledgement covering a found user was made, or nil: one of
// its identity that holds every access key the check found on it. Only a bounded peer's user is
// acknowledged, as UnacknowledgedPeerBackupStorageUsers reads it.
func acknowledgement(account uuid.UUID, u api.BackupStorageFoundUser, acks []db.BackupStorageAcknowledgementsRow) *time.Time {
	if !u.Peer || !u.Bounded {
		return nil
	}
	for _, a := range acks {
		if a.AccountID != account || a.ID != u.Id {
			continue
		}
		for _, k := range u.AccessKeyIds {
			if !slices.Contains(a.AccessKeyIds, k) {
				return nil
			}
		}
		at := a.AcknowledgedAt
		return &at
	}
	return nil
}

// accountView is an account as its latest check found it, with the found users management is
// still revoking, which a check no longer reports once their rows make them known, and why each
// revocation last failed.
func accountView(ws, installation uuid.UUID, row db.ListBackupStorageAccountsRow, revoking []db.RevokingFoundBackupStorageKeysRow, acks []db.BackupStorageAcknowledgementsRow) (api.BackupStorageAccount, error) {
	var report accountReport
	if len(row.Report) > 0 {
		if err := json.Unmarshal(row.Report, &report); err != nil {
			return api.BackupStorageAccount{}, err
		}
	}
	if report.Found == nil {
		report.Found = []api.BackupStorageFoundUser{}
	}
	for _, k := range revoking {
		if k.AccountID != row.ID {
			continue
		}
		var tags map[string]string
		if err := json.Unmarshal(k.FoundTags, &tags); err != nil {
			return api.BackupStorageAccount{}, err
		}
		j := slices.IndexFunc(report.Found, func(f api.BackupStorageFoundUser) bool { return f.UserName == k.UserName })
		if j < 0 {
			// Only a bounded user can be revoked.
			report.Found = append(report.Found, api.BackupStorageFoundUser{UserName: k.UserName, InstallationId: tags["astilba:installation"], ServerId: tags["astilba:server"], Bounded: true})
			j = len(report.Found) - 1
		}
		report.Found[j].Revoking, report.Found[j].LastError = true, textPointer(k.LastError)
	}
	// A peer added since the check is shown as one at once, as revoking its users is refused.
	for i := range report.Found {
		report.Found[i].Peer = isPeer(report.Found[i].InstallationId, row.Peers)
		report.Found[i].AcknowledgedAt = acknowledgement(row.ID, report.Found[i], acks)
		if report.Found[i].AccessKeyIds == nil {
			report.Found[i].AccessKeyIds = []string{}
		}
	}
	peers := slices.Clone(row.Peers)
	if peers == nil {
		peers = []uuid.UUID{}
	}
	return api.BackupStorageAccount{Id: row.ID, Name: row.Name, AwsAccount: row.AwsAccount, AdminArn: row.AdminArn, Boundary: row.Boundary, Path: awskeys.Path(ws.String()), InstallationId: installation, Generation: row.Generation, Peers: peers, CheckedAt: row.ReportedAt, Checking: row.Checking, CheckError: textPointer(row.CheckError), FoundUsers: report.Found, ExtraKeysDeleted: report.ExtraKeysDeleted}, nil
}

// CheckBackupStorageAccount asks for the account check to run on the next pass.
func (s *Service) CheckBackupStorageAccount(ctx context.Context, p store.Principal, ws, account uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true}, "backup.account.check", account, func(tx pgx.Tx) error {
		if _, err := db.New(tx).BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: account}); errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		} else if err != nil {
			return err
		}
		if err := db.New(tx).RequestBackupStorageAccountCheck(ctx, db.RequestBackupStorageAccountCheckParams{WorkspaceID: ws, ID: account}); err != nil {
			return err
		}
		return s.queueAccountCheck(ctx, tx)
	})
}

func (s *Service) AddBackupStorageAccountPeer(ctx context.Context, p store.Principal, ws, account uuid.UUID, in api.BackupStorageAccountPeerRequest) error {
	return s.scope(ctx, p, ws, ownerSettings, "backup.account.peer", account, func(tx pgx.Tx) error {
		q := db.New(tx)
		self, err := q.InstallationID(ctx)
		if err != nil {
			return err
		}
		if in.InstallationId == self || in.InstallationId == uuid.Nil {
			return domain.Invalid("installationId")
		}
		row, err := q.BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: account})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		} else if err != nil {
			return err
		}
		n, err := q.AddBackupStorageAccountPeer(ctx, db.AddBackupStorageAccountPeerParams{WorkspaceID: ws, ID: account, Peer: in.InstallationId})
		if err != nil {
			return err
		}
		if n == 0 {
			if slices.Contains(row.Peers, in.InstallationId) {
				return nil
			}
			return errPeerLimit
		}
		// A check now shows the peer's users as its, so the alert they raised clears.
		return errors.Join(q.RequestBackupStorageAccountCheck(ctx, db.RequestBackupStorageAccountCheckParams{WorkspaceID: ws, ID: account}), s.queueAccountCheck(ctx, tx))
	})
}

// RevokeFoundBackupStorageUser latches a user the account check found and reported, for the sweep
// to revoke. Only a user the latest check reported can be named, so this can't reach a user
// another installation's records hold, or one outside the workspace's path.
func (s *Service) RevokeFoundBackupStorageUser(ctx context.Context, p store.Principal, ws, account uuid.UUID, user string) error {
	return s.scope(ctx, p, ws, ownerSettings, "backup.key.revoke", account, func(tx pgx.Tx) error {
		q := db.New(tx)
		rows, err := q.ListBackupStorageAccounts(ctx, ws)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows, func(r db.ListBackupStorageAccountsRow) bool { return r.ID == account })
		if i < 0 {
			return domain.NotFound
		}
		var report accountReport
		if len(rows[i].Report) > 0 && json.Unmarshal(rows[i].Report, &report) != nil {
			return domain.Conflict
		}
		j := slices.IndexFunc(report.Found, func(f api.BackupStorageFoundUser) bool { return f.UserName == user })
		if j < 0 || strings.TrimSpace(user) == "" {
			return domain.NotFound
		}
		// The admin policy can only change bounded users, so revoking one without would never end.
		if !report.Found[j].Bounded {
			return errFoundUnbounded
		}
		// A peer's user is that installation's to revoke: its key may be one a transferred
		// server still uses. The account's peers are read as they are now, not as the check saw
		// them.
		if isPeer(report.Found[j].InstallationId, rows[i].Peers) {
			return errFoundPeer
		}
		tags, _ := stablejson.Marshal(map[string]string{"astilba:installation": report.Found[j].InstallationId, "astilba:server": report.Found[j].ServerId})
		if err = q.InsertFoundBackupStorageKey(ctx, db.InsertFoundBackupStorageKeyParams{ID: uuid.New(), WorkspaceID: ws, AccountID: account, UserName: user, FoundTags: tags}); err != nil {
			return err
		}
		// The account shows the user as revoking from its row. A check now finds the user known,
		// so the alert it raised clears without waiting a day.
		return errors.Join(q.RequestBackupStorageAccountCheck(ctx, db.RequestBackupStorageAccountCheckParams{WorkspaceID: ws, ID: account}), s.queueKeySweep(ctx, tx), s.queueAccountCheck(ctx, tx))
	})
}

// AcknowledgeFoundBackupStorageUser records that a user the account check found tagged with one
// of the account's peers is that installation's, which clears the backup_peer_user alert it
// raised. The owner names the user and what they reviewed of it: its IAM user ID and access keys,
// which must be what the latest check found. The acknowledgement covers that identity and those
// keys alone, so a user made again under the name, or given another key, raises the alert again.
func (s *Service) AcknowledgeFoundBackupStorageUser(ctx context.Context, p store.Principal, ws, account uuid.UUID, user string, in api.BackupStorageAcknowledgementRequest) error {
	id := foundUserID(account, user, in.UserId)
	return s.scope(ctx, p, ws, ownerSettings, "backup.key.acknowledge", id, func(tx pgx.Tx) error {
		q := db.New(tx)
		rows, err := q.ListBackupStorageAccounts(ctx, ws)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows, func(r db.ListBackupStorageAccountsRow) bool { return r.ID == account })
		if i < 0 {
			return domain.NotFound
		}
		var report accountReport
		if len(rows[i].Report) > 0 && json.Unmarshal(rows[i].Report, &report) != nil {
			return domain.Conflict
		}
		j := slices.IndexFunc(report.Found, func(f api.BackupStorageFoundUser) bool { return f.UserName == user })
		if j < 0 || strings.TrimSpace(user) == "" {
			return domain.NotFound
		}
		found := report.Found[j]
		if !found.Bounded {
			return errAcknowledgeUnbounded
		}
		// The account's peers are read as they are now, as the alert reads them.
		if !isPeer(found.InstallationId, rows[i].Peers) {
			return errAcknowledgeNotPeer
		}
		reviewed := slices.Sorted(slices.Values(in.AccessKeyIds))
		if found.Id != id || found.UserId != in.UserId || !slices.Equal(slices.Sorted(slices.Values(found.AccessKeyIds)), slices.Compact(reviewed)) {
			return errFoundChanged
		}
		return q.AcknowledgeBackupStorageUser(ctx, db.AcknowledgeBackupStorageUserParams{WorkspaceID: ws, ID: id, AccountID: account, UserName: user, IamUserID: in.UserId, AccessKeyIds: slices.Compact(reviewed)})
	})
}

// writeOnly is a write-only request field's value; the contract leaves it optional only so that
// responses can leave it out.
func writeOnly(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
