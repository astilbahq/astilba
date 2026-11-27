package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// A renewed server's managed destination lost its key with the revocation. The re-mint gives it a
// new one through the same two transactions as a new destination, then moves each of its policies
// to a new generation and installs it, since an agent ignores an install of the generation it has.
// The policies keep their repositories and sealed passwords. See docs/backups.md#managed-keys.
type remintArgs struct {
	Workspace   uuid.UUID `json:"workspace"`
	Destination uuid.UUID `json:"destination"`
}

func (remintArgs) Kind() string { return "platform_backup_remint_v1" }

var remintOpts = river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateScheduled, rivertype.JobStateRetryable}}}

type remintWorker struct {
	river.WorkerDefaults[remintArgs]
	service *Service
}

// A mint waits through IAM's propagation delay, up to its deadline.
func (w *remintWorker) Timeout(*river.Job[remintArgs]) time.Duration {
	return mintDeadline + time.Minute
}

var remintPrincipal = secretbroker.System("backup-remint")

func (w *remintWorker) Work(ctx context.Context, job *river.Job[remintArgs]) error {
	s, ws, destination := w.service, job.Args.Workspace, job.Args.Destination
	key, err := s.startRemint(ctx, ws, destination)
	if errors.Is(err, errNoRemint) {
		return nil
	} else if err != nil {
		return s.remintFailed(ctx, ws, destination, err)
	}
	m, err := s.mintKey(ctx, remintPrincipal, ws, key)
	defer m.clear()
	if err == nil {
		err = s.finishRemint(ctx, ws, destination, m)
	}
	if err != nil {
		if latch := s.latchKey(ctx, ws, key, err); latch != nil {
			return latch
		}
		return s.remintFailed(ctx, ws, destination, err)
	}
	return s.Store.WorkspaceWriteTransaction(context.WithoutCancel(ctx), ws, func(tx pgx.Tx) error {
		return db.New(tx).RecordBackupRemint(ctx, db.RecordBackupRemintParams{WorkspaceID: ws, ID: destination})
	})
}

var errNoRemint = errors.New("the destination doesn't need a new key")

func (s *Service) remintFailed(ctx context.Context, ws, destination uuid.UUID, cause error) error {
	return s.Store.WorkspaceWriteTransaction(context.WithoutCancel(ctx), ws, func(tx pgx.Tx) error {
		return db.New(tx).RecordBackupRemintFailure(ctx, db.RecordBackupRemintFailureParams{WorkspaceID: ws, ID: destination, RemintError: fixedText(errorCode(cause))})
	})
}

// startRemint records the new key as minting, in the workspace's turn, once the destination still
// needs one: its server isn't revoked and no key is active or being minted for it.
func (s *Service) startRemint(ctx context.Context, ws, destination uuid.UUID) (uuid.UUID, error) {
	key := uuid.New()
	err := s.Store.WorkspaceWriteTransaction(ctx, ws, func(tx pgx.Tx) error {
		q := db.New(tx)
		d, err := q.ManagedBackupDestination(ctx, db.ManagedBackupDestinationParams{WorkspaceID: ws, ID: destination})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoRemint
		} else if err != nil {
			return err
		}
		if d.ServerID == nil || d.AccountID == nil {
			return errNoRemint
		}
		if revoked, err := serverRevoked(ctx, tx, ws, *d.ServerID); err != nil || revoked {
			return errors.Join(err, errNoRemint)
		}
		if live, err := q.LiveServerKeyExists(ctx, db.LiveServerKeyExistsParams{WorkspaceID: ws, DestinationID: &destination}); err != nil || live {
			return errors.Join(err, errNoRemint)
		}
		var config backup.Destination
		if err = json.Unmarshal(d.Configuration, &config); err != nil {
			return err
		}
		scope := config.KeyScope(ws)
		return q.InsertBackupStorageKey(ctx, db.InsertBackupStorageKeyParams{ID: key, WorkspaceID: ws, AccountID: *d.AccountID, DestinationID: &destination, HolderServerID: d.ServerID, Purpose: "server", Scope: fixedText(scope), UserName: keyUserName(key)})
	})
	if errors.Is(err, errNoRemint) {
		return uuid.Nil, errNoRemint
	}
	return key, err
}

// finishRemint activates the new key and moves each of the destination's policies past the
// generation its server has, recording those generations on the key, whose older keys are
// superseded once the server reports them. Management queues the installs itself (see
// queueKeyInstalls), so a coordinated schedule, which runs only at its installed generation,
// never stops waiting for the owner.
func (s *Service) finishRemint(ctx context.Context, ws, destination uuid.UUID, m minted) error {
	session, err := s.activation(ctx, remintPrincipal, ws, m.key)
	if err != nil {
		return err
	}
	err = s.Store.WorkspaceWriteTransaction(context.WithoutCancel(ctx), ws, func(tx pgx.Tx) error {
		q := db.New(tx)
		rows, err := q.DestinationPolicyGenerations(ctx, db.DestinationPolicyGenerationsParams{WorkspaceID: ws, DestinationID: destination.String()})
		if err != nil {
			return err
		}
		installs := make([]keyInstall, 0, len(rows))
		for _, row := range rows {
			var p backup.Policy
			if err = json.Unmarshal(row.Configuration, &p); err != nil {
				return err
			}
			// A restore can leave the configuration behind what the agent has, so the new
			// generation is past both.
			p.Generation = max(p.Generation, row.Reported) + 1
			raw, err := stablejson.Marshal(p)
			if err != nil {
				return err
			}
			if err = q.UpdateBackupPolicyConfiguration(ctx, db.UpdateBackupPolicyConfigurationParams{WorkspaceID: ws, ID: row.ID, Configuration: raw}); err != nil {
				return err
			}
			installs = append(installs, keyInstall{Policy: row.ID, Generation: p.Generation})
		}
		ok, err := s.activateKey(ctx, tx, session, ws, m, installs, nil)
		if err != nil {
			return err
		}
		if !ok {
			return errKeyNotMinting
		}
		return nil
	})
	session.Close(ctx, secretbroker.UsedIf(err == nil))
	return err
}

// installRequestKey is the request key of management's install of a policy generation for a key,
// so retrying it never queues a second one.
func installRequestKey(key, policy uuid.UUID, generation int64) uuid.UUID {
	raw, _ := stablejson.Marshal(keyInstall{Policy: policy, Generation: generation})
	return uuid.NewSHA1(key, raw)
}

// queueKeyInstalls queues the installs re-mints are waiting for. An environment runs one operation
// at a time, so an install that can't start now is tried again on a later pass.
func (s *Service) queueKeyInstalls(ctx context.Context) error {
	rows, err := s.Store.Queries.PendingKeyInstalls(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil
		}
		err = s.Store.WorkspaceWriteTransaction(ctx, row.WorkspaceID, func(tx pgx.Tx) error {
			_, err := s.backupCommand(ctx, tx, row.WorkspaceID, row.EnvironmentID, row.PolicyID, api.BackupCommandRequest{Action: "install", RequestKey: installRequestKey(row.KeyID, row.PolicyID, row.Generation)})
			return err
		})
		var refused *domain.Error
		if err != nil && !errors.As(err, &refused) {
			return err
		}
	}
	return nil
}
