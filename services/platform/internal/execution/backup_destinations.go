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
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Service) BackupDestinations(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.BackupDestination, error) {
	out := []api.BackupDestination{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := db.New(tx).ListBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		exposed, err := db.New(tx).ExposedBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		policies, err := db.New(tx).BackupDestinationPolicies(ctx, ws)
		if err != nil {
			return err
		}
		keys, err := destinationKeys(ctx, tx, ws)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var v backup.Destination
			if err = json.Unmarshal(row.Configuration, &v); err != nil {
				return err
			}
			view := destinationView(v, row.ServerID, row.Shared, slices.Contains(exposed, v.ID))
			if row.AccountID != nil {
				view.AccountId, view.Keys, view.RemintError, view.Placeholder = row.AccountID, keys[v.ID], textPointer(row.RemintError), row.Placeholder
				view.RotationRequested, view.RotationDueAt = row.RotationRequested, keyRotationDue(row)
			}
			for _, p := range policies {
				if p.DestinationID == v.ID {
					view.Policies = append(view.Policies, api.BackupDestinationPolicy{Id: p.ID, Name: p.Name, ProjectId: p.ProjectID, EnvironmentId: p.EnvironmentID, EnvironmentName: p.EnvironmentName})
				}
			}
			out = append(out, view)
		}
		return nil
	})
	return out, err
}

// keyRotationDue is when a managed destination's account rotates its key, as ClaimDueBackupRemint
// reads key_rotation_days: while it holds one active key, on a server that isn't revoked.
func keyRotationDue(row db.ListBackupDestinationsRow) *time.Time {
	if !row.KeyRotationDays.Valid || row.ActiveKeys != 1 || row.ServerRevoked || row.KeyActivatedAt == nil {
		return nil
	}
	due := row.KeyActivatedAt.Add(time.Duration(row.KeyRotationDays.Int32) * 24 * time.Hour)
	return &due
}

// destinationView is d, bound to server or shared, as the API shows it.
func destinationView(d backup.Destination, server *uuid.UUID, shared, exposed bool) api.BackupDestination {
	v := api.BackupDestination{Id: d.ID, Name: d.Name, Kind: api.BackupDestinationKind(d.Kind), Root: d.Root, Endpoint: d.Endpoint, Bucket: d.Bucket, Region: d.Region, ServerId: server, Shared: shared, Exposed: exposed, Policies: []api.BackupDestinationPolicy{}, Keys: []api.BackupStorageKey{}}
	if d.Prefix != "" {
		v.Prefix = &d.Prefix
	}
	return v
}

// CreateBackupDestination adds destination d with access a. An S3 destination is bound to server,
// the deployment server its keys are given to, and a filesystem destination has none.
func (s *Service) CreateBackupDestination(ctx context.Context, p store.Principal, ws uuid.UUID, d backup.Destination, a backup.Access, server *uuid.UUID) (api.BackupDestination, error) {
	d.ID = uuid.New()
	a.Password = ""
	if d.Kind == "s3" && d.Prefix != "" && !backup.ValidPrefix(d.Prefix) {
		return api.BackupDestination{}, domain.Invalid("prefix")
	}
	if backup.ValidateDestination(d, a) != nil {
		return api.BackupDestination{}, domain.Invalid("destination")
	}
	if d.Kind == "s3" {
		if field := d.Unknowable(); field != "" {
			return api.BackupDestination{}, domain.Invalid(field)
		}
	}
	if (d.Kind == "s3") != (server != nil) {
		return api.BackupDestination{}, domain.Invalid("serverId")
	}
	item := secretbroker.Item{Kind: secretbroker.BackupDestination, ID: d.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return api.BackupDestination{}, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.destination.create", d.ID, func(tx pgx.Tx) error {
		n, err := db.New(tx).CountBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		if n >= 100 {
			return domain.Conflict
		}
		if server != nil {
			if err = s.admitDestination(ctx, tx, ws, *server, d); err != nil {
				return err
			}
		}
		secret, err := sealIntegration(ctx, db.New(tx), keys, item, a)
		if err != nil {
			return err
		}
		sealed = true
		raw, _ := stablejson.Marshal(d)
		return db.New(tx).InsertBackupDestination(ctx, db.InsertBackupDestinationParams{ID: d.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: secret, ServerID: server})
	})
	if err != nil {
		return api.BackupDestination{}, err
	}
	// No policy uses a new destination yet.
	return destinationView(d, server, false, false), nil
}

// CreateManagedBackupDestination adds S3 destination d on AWS, bound to server, whose key
// management mints with storage account account rather than taking one from the owner. It runs in
// two transactions around the AWS calls, so no lock is held while IAM propagates: the first records
// the destination and its minting key, and the second activates the key. A mint that fails leaves
// the key latched; the sweep revokes whatever AWS made and then deletes the destination, which no
// policy can use before its key is active. See docs/backups.md#managed-keys.
func (s *Service) CreateManagedBackupDestination(ctx context.Context, p store.Principal, ws uuid.UUID, d backup.Destination, account, server uuid.UUID) (api.BackupDestination, error) {
	d.ID = uuid.New()
	if d.Kind != "s3" {
		return api.BackupDestination{}, domain.Invalid("kind")
	}
	if d.Endpoint != "" {
		// The endpoint is AWS's own for the region, never one management is told to call.
		return api.BackupDestination{}, domain.Invalid("endpoint")
	}
	if !awskeys.ValidRegion(d.Region) {
		return api.BackupDestination{}, domain.Invalid("region")
	}
	d.Endpoint = backup.ManagedEndpoint(d.Region)
	if d.Prefix != "" && !backup.ValidPrefix(d.Prefix) {
		return api.BackupDestination{}, domain.Invalid("prefix")
	}
	// The destination's own access stays empty: its key lives on its key rows.
	if backup.ValidateDestination(d, backup.Access{AccessKey: "managed", SecretKey: "managed"}) != nil {
		return api.BackupDestination{}, domain.Invalid("destination")
	}
	// The endpoint is AWS's, so only an access point's alias as the bucket is refused here.
	if field := d.Unknowable(); field != "" {
		return api.BackupDestination{}, domain.Invalid(field)
	}
	key := uuid.New()
	item := secretbroker.Item{Kind: secretbroker.BackupDestination, ID: d.ID}
	keys, done, err := s.sealing(ctx, p, ws, item)
	if err != nil {
		return api.BackupDestination{}, err
	}
	sealed := false
	defer func() { done(sealed) }()
	err = s.scope(ctx, p, ws, ownerSettings, "backup.destination.create", d.ID, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.CountBackupDestinations(ctx, ws)
		if err != nil {
			return err
		}
		if n >= 100 {
			return domain.Conflict
		}
		if _, err = q.BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: ws, ID: account}); errors.Is(err, pgx.ErrNoRows) {
			return domain.Invalid("accountId")
		} else if err != nil {
			return err
		}
		if err = s.admitDestination(ctx, tx, ws, server, d); err != nil {
			return err
		}
		secret, err := sealIntegration(ctx, q, keys, item, backup.Access{})
		if err != nil {
			return err
		}
		sealed = true
		raw, err := stablejson.Marshal(d)
		if err != nil {
			return err
		}
		if err = q.InsertBackupDestination(ctx, db.InsertBackupDestinationParams{ID: d.ID, WorkspaceID: ws, Configuration: raw, EncryptedAccess: secret, ServerID: &server, AccountID: &account}); err != nil {
			return err
		}
		return q.InsertBackupStorageKey(ctx, db.InsertBackupStorageKeyParams{ID: key, WorkspaceID: ws, AccountID: account, DestinationID: &d.ID, HolderServerID: &server, Purpose: "server", Scope: fixedText(d.KeyScope(ws)), UserName: keyUserName(key)})
	})
	if err != nil {
		return api.BackupDestination{}, err
	}
	// Once the key is recorded, the create finishes even if the owner's request ends: the key is
	// either activated or latched, never left minting.
	work := context.WithoutCancel(ctx)
	m, err := s.mintKey(work, p.Secrets(), ws, key)
	defer m.clear()
	var session *secretbroker.Session
	if err == nil {
		session, err = s.activation(work, p.Secrets(), ws, key)
	}
	if err == nil {
		err = s.Store.WorkspaceWriteTransaction(work, ws, func(tx pgx.Tx) error {
			ok, err := s.activateKey(work, tx, session, ws, m, nil, nil)
			if err == nil && !ok {
				err = errKeyNotMinting
			}
			return err
		})
		session.Close(work, secretbroker.UsedIf(err == nil))
	}
	if err != nil {
		if latch := s.latchKey(work, ws, key, err); latch != nil {
			return api.BackupDestination{}, errors.Join(latch, err)
		}
		return api.BackupDestination{}, mintError(err)
	}
	// The view is the list's, so it shows the key's rotation as every later read does.
	list, err := s.BackupDestinations(ctx, p, ws)
	if i := slices.IndexFunc(list, func(v api.BackupDestination) bool { return v.Id == d.ID }); err == nil && i >= 0 {
		return list[i], nil
	} else if err == nil {
		err = domain.NotFound
	}
	return api.BackupDestination{}, err
}

var (
	errMintProbe   = &domain.Error{Code: "invalid_input", Message: "AWS made the destination's key, but it couldn't list the bucket inside its prefix. Check the bucket and region, and that the permissions boundary allows S3 there. The key is being revoked.", Fields: []string{"bucket"}}
	errMintRevoked = &domain.Error{Code: "conflict", Message: "The destination's server was revoked while its key was being made. The key is being revoked."}
	errMintRefused = &domain.Error{Code: "invalid_input", Message: "AWS refused to make the destination's key with this storage account. Check its admin policy in docs/backups.md#managed-keys. Nothing it made is kept.", Fields: []string{"accountId"}}
	errMintFailed  = &domain.Error{Code: "provider_unavailable", Message: "AWS could not be reached, or failed, while making the destination's key. Nothing it made is kept. Try again shortly."}
	errKeyProbe    = errors.New("the minted key failed its probe")
)

// mintError is the owner's error for a failed mint, naming the step that failed.
func mintError(err error) error {
	switch {
	case errors.Is(err, errKeyProbe):
		return errMintProbe
	case errors.Is(err, errKeyNotMinting):
		return errMintRevoked
	case errors.Is(err, awskeys.ErrCredential), errors.Is(err, awskeys.ErrDenied), errors.Is(err, awskeys.ErrNotAdmin), errors.Is(err, errAccountMoved):
		return errMintRefused
	}
	var refused *domain.Error
	if errors.As(err, &refused) {
		return err
	}
	return errMintFailed
}

// destinationKeys are the workspace's managed destinations' keys as their views show them: those
// not yet revoked, newest first, then the most recently revoked.
func destinationKeys(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (map[uuid.UUID][]api.BackupStorageKey, error) {
	rows, err := db.New(tx).DestinationBackupStorageKeys(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]api.BackupStorageKey{}
	for _, r := range rows {
		out[*r.DestinationID] = append(out[*r.DestinationID], api.BackupStorageKey{Id: r.ID, Purpose: api.BackupStorageKeyPurpose(r.Purpose), State: api.BackupStorageKeyState(r.State), UserName: r.UserName, ServerId: *r.HolderServerID, HandedOver: r.HandedOver, ActivatedAt: r.ActivatedAt, RevokedAt: r.RevokedAt, Attempts: int(r.Attempts), LastError: textPointer(r.LastError)})
	}
	return out, nil
}

// managedAccess is a managed destination's key for a backup command on server: its current key,
// and only while server holds it.
func (s *Service) managedAccess(ctx context.Context, keys valueKeys, ws, destination, server uuid.UUID) (backup.Access, error) {
	k, err := s.Store.Queries.CurrentBackupStorageKey(ctx, db.CurrentBackupStorageKeyParams{WorkspaceID: ws, DestinationID: &destination})
	if errors.Is(err, pgx.ErrNoRows) {
		return backup.Access{}, errNoManagedKey
	} else if err != nil {
		return backup.Access{}, err
	}
	if k.HolderServerID == nil || *k.HolderServerID != server {
		return backup.Access{}, errNoManagedKey
	}
	var a mintedAccess
	if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupStorageKey, ID: k.ID}, k.EncryptedSecret, &a); err != nil {
		return backup.Access{}, err
	}
	return backup.Access{AccessKey: a.AccessKey, SecretKey: a.SecretKey}, nil
}

// errNoManagedKey is a backup command whose server holds no key for its managed destination: a
// renewed server waiting for its re-mint, or one the destination isn't bound to.
var errNoManagedKey = &domain.Error{Code: "conflict", Message: "This server has no storage key for the backup destination yet. Management is making one."}

// RevokeBackupStorageKey latches one of a managed destination's keys for the sweep to revoke: a
// handed-over key the new installation no longer needs, or the current key, which a re-mint then
// replaces.
func (s *Service) RevokeBackupStorageKey(ctx context.Context, p store.Principal, ws, destination, key uuid.UUID) error {
	return s.scope(ctx, p, ws, ownerSettings, "backup.key.revoke", key, func(tx pgx.Tx) error {
		q := db.New(tx)
		k, err := q.BackupStorageKey(ctx, db.BackupStorageKeyParams{WorkspaceID: ws, ID: key})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (k.DestinationID == nil || *k.DestinationID != destination) {
			return domain.NotFound
		} else if err != nil {
			return err
		}
		if _, err = q.LatchBackupStorageKey(ctx, db.LatchBackupStorageKeyParams{WorkspaceID: ws, ID: key, LastError: pgtype.Text{}}); err != nil {
			return err
		}
		return s.queueKeySweep(ctx, tx)
	})
}

var (
	errRotateRevoked = &domain.Error{Code: "conflict", Message: "This destination's server is revoked, so its keys aren't replaced."}
	errRotateNoKey   = &domain.Error{Code: "conflict", Message: "This destination has no key in use to replace."}
	errRotating      = &domain.Error{Code: "conflict", Message: "This destination's key is already being replaced. Its old key is revoked once its server installs the new one."}
	errRotateAsked   = &domain.Error{Code: "conflict", Message: "A new key for this destination has already been asked for. If making it keeps failing, fix the cause and retry."}
)

// RotateBackupStorageKey asks for a managed destination's key to be replaced without revoking its
// server. The request stands until a key activates after it, so the sweep's re-mint tries again
// after a failure, and the old key is revoked once the server has installed the new one.
func (s *Service) RotateBackupStorageKey(ctx context.Context, p store.Principal, ws, destination uuid.UUID) error {
	return s.scope(ctx, p, ws, ownerSettings, "backup.key.rotate", destination, func(tx pgx.Tx) error {
		q := db.New(tx)
		d, err := q.ManagedBackupDestination(ctx, db.ManagedBackupDestinationParams{WorkspaceID: ws, ID: destination})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		} else if err != nil {
			return err
		}
		if revoked, err := serverRevoked(ctx, tx, ws, *d.ServerID); err != nil {
			return err
		} else if revoked {
			return errRotateRevoked
		}
		k, err := q.BackupDestinationKeyState(ctx, db.BackupDestinationKeyStateParams{WorkspaceID: ws, ID: destination})
		switch {
		case err != nil:
			return err
		case k.Minting > 0 || k.Active > 1:
			return errRotating
		case k.Active == 0:
			return errRotateNoKey
		case k.Requested:
			return errRotateAsked
		}
		if err = q.RequestBackupRekey(ctx, db.RequestBackupRekeyParams{WorkspaceID: ws, ID: destination}); err != nil {
			return err
		}
		return s.queueKeySweep(ctx, tx)
	})
}

// RetryBackupStorageKeys has a managed destination's failing revocations and re-mint tried again
// now, once the owner has fixed what made them fail.
func (s *Service) RetryBackupStorageKeys(ctx context.Context, p store.Principal, ws, destination uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true}, "backup.key.retry", destination, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.ManagedBackupDestination(ctx, db.ManagedBackupDestinationParams{WorkspaceID: ws, ID: destination}); errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		} else if err != nil {
			return err
		}
		if _, err := q.RetryBackupStorageKeys(ctx, db.RetryBackupStorageKeysParams{WorkspaceID: ws, DestinationID: &destination}); err != nil {
			return err
		}
		if err := q.RetryBackupRemint(ctx, db.RetryBackupRemintParams{WorkspaceID: ws, ID: destination}); err != nil {
			return err
		}
		return s.queueKeySweep(ctx, tx)
	})
}
