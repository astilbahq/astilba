package execution

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Revoking a server revokes its keys, so a restore from a managed destination can't use the lost
// server's. It mints its own for the new primary, limited to the one policy's repository, in two
// transactions around the AWS calls: the first records the key as minting under the restore's
// request key, and the second runs the restore's checks again, then inserts its operation and
// activates the key together, so no poll ever sees the operation without its key. The key is
// revoked once the operation ends, however it ends. See docs/backups.md#managed-keys.
type recoveryKeyStart struct {
	key    uuid.UUID
	server uuid.UUID
}

var (
	errRecoveryMinting = &domain.Error{Code: "conflict", Message: "This restore is still starting: its storage key is being made."}
	errRecoveryChanged = &domain.Error{Code: "conflict", Message: "The environment changed while the restore's storage key was being made. Start the restore again."}
)

// startRecoveryKey records a managed restore's key as minting, in the restore's first transaction.
// An earlier start with the same request key that is still minting is in progress; one whose key
// was latched without an operation is tried again with a new key.
func (s *Service) startRecoveryKey(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, in api.BackupRecoveryRequest, start recoveryStart) (*recoveryKeyStart, error) {
	q := db.New(tx)
	request := fixedText(in.RequestKey.String())
	prior, err := q.RecoveryStartKey(ctx, db.RecoveryStartKeyParams{WorkspaceID: ws, RecoveryRequest: request})
	if err == nil {
		if prior.RecoveryPolicyID == nil || *prior.RecoveryPolicyID != id || prior.RecoverySnapshot.String != in.Snapshot {
			return nil, domain.Conflict
		}
		if prior.State == "minting" || prior.State == "active" {
			return nil, errRecoveryMinting
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	key := uuid.New()
	d, policy := start.recovery.Installation.Destination, start.recovery.Installation.Policy
	err = q.InsertBackupStorageKey(ctx, db.InsertBackupStorageKeyParams{ID: key, WorkspaceID: ws, AccountID: *start.account, DestinationID: &d.ID, HolderServerID: &start.server, Purpose: "recovery", Scope: fixedText(d.PolicyScope(policy)), UserName: keyUserName(key), RecoveryRequest: request, RecoveryPolicyID: &id, RecoverySnapshot: fixedText(in.Snapshot)})
	if err != nil {
		return nil, err
	}
	return &recoveryKeyStart{key: key, server: start.server}, nil
}

// finishRecoveryKey mints a managed restore's key with no locks held, then starts the restore.
func (s *Service) finishRecoveryKey(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.BackupRecoveryRequest, k recoveryKeyStart) (api.Operation, error) {
	m, err := s.mintKey(ctx, p.Secrets(), ws, k.key)
	defer m.clear()
	work := context.WithoutCancel(ctx)
	var out api.Operation
	var session *secretbroker.Session
	if err == nil {
		session, err = s.activation(work, p.Secrets(), ws, k.key)
	}
	if err == nil {
		err = s.scope(work, p, ws, recoveryAccess, "", uuid.Nil, func(tx pgx.Tx) error {
			start, err := s.checkRecovery(work, tx, ws, pid, env, id, in)
			if err != nil {
				return err
			}
			if start.account == nil || start.server != k.server {
				return errRecoveryChanged
			}
			if out, err = s.insertRecovery(work, tx, ws, env, id, in, start); err != nil {
				return err
			}
			ok, err := s.activateKey(work, tx, session, ws, m, nil, &start.recovery.RunID)
			if err == nil && !ok {
				err = errKeyNotMinting
			}
			return err
		})
		session.Close(work, secretbroker.UsedIf(err == nil))
	}
	if err != nil {
		if latch := s.latchKey(work, ws, k.key, err); latch != nil {
			return api.Operation{}, errors.Join(latch, err)
		}
		return api.Operation{}, mintError(err)
	}
	return out, nil
}

// openRecoveryAccess fills a released restore's repository and destination access. A managed
// destination's access is the restore's own key, and only for the server it was minted for.
func openRecoveryAccess(ctx context.Context, q *db.Queries, keys valueKeys, ws uuid.UUID, release recoveryRelease, r *backup.Recovery, c *api.ExecutionCommand) error {
	policy := release.policy
	if err := openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: c.ResourceId}, release.encrypted, &r.Installation.Access); err != nil {
		return err
	}
	destination, err := q.BackupDestinationAccess(ctx, db.BackupDestinationAccessParams{WorkspaceID: ws, ID: policy.DestinationID})
	if err != nil {
		return err
	}
	if err = json.Unmarshal(destination.Configuration, &r.Installation.Destination); err != nil {
		return err
	}
	var a backup.Access
	if destination.AccountID != nil {
		k, err := q.RecoveryBackupStorageKey(ctx, db.RecoveryBackupStorageKeyParams{WorkspaceID: ws, RecoveryID: &r.RunID})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (k.HolderServerID == nil || *k.HolderServerID != c.ServerId) {
			return errNoManagedKey
		} else if err != nil {
			return err
		}
		var minted mintedAccess
		if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupStorageKey, ID: k.ID}, k.EncryptedSecret, &minted); err != nil {
			return err
		}
		a = backup.Access{AccessKey: minted.AccessKey, SecretKey: minted.SecretKey}
	} else if err = openJSON(keys, secretbroker.Item{Kind: secretbroker.BackupDestination, ID: policy.DestinationID}, destination.EncryptedAccess, &a); err != nil {
		return err
	}
	r.Installation.Access.AccessKey, r.Installation.Access.SecretKey = a.AccessKey, a.SecretKey
	raw, _ := stablejson.Marshal(r)
	value := jsontext.Value(raw)
	c.Backup = &value
	return nil
}
