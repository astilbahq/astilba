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
	// Once the key is recorded, the job finishes it even if River stops the job: the key is either
	// activated or latched, never left minting.
	work := context.WithoutCancel(ctx)
	m, err := s.mintKey(work, remintPrincipal, ws, key)
	defer m.clear()
	if err == nil {
		err = s.finishRemint(work, ws, destination, m)
	}
	if err != nil {
		if latch := s.latchKey(work, ws, key, err); latch != nil {
			return latch
		}
		return s.remintFailed(work, ws, destination, err)
	}
	return s.Store.WorkspaceWriteTransaction(work, ws, func(tx pgx.Tx) error {
		return db.New(tx).RecordBackupRemint(work, db.RecordBackupRemintParams{WorkspaceID: ws, ID: destination})
	})
}

var errNoRemint = errors.New("the destination doesn't need a new key")

func (s *Service) remintFailed(ctx context.Context, ws, destination uuid.UUID, cause error) error {
	work := context.WithoutCancel(ctx)
	return s.Store.WorkspaceWriteTransaction(work, ws, func(tx pgx.Tx) error {
		return db.New(tx).RecordBackupRemintFailure(work, db.RecordBackupRemintFailureParams{WorkspaceID: ws, ID: destination, RemintError: fixedText(errorCode(cause))})
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
// generation its server has, recording those generations on the key. The old key was latched when
// its server was revoked; the generations let the sweep supersede an older active key only where a
// management recovery merged two histories that each re-minted. Management queues the installs itself (see
// queueKeyInstalls), so a coordinated schedule, which runs only at its installed generation,
// never stops waiting for the owner.
func (s *Service) finishRemint(ctx context.Context, ws, destination uuid.UUID, m minted) error {
	session, err := s.activation(ctx, remintPrincipal, ws, m.key)
	if err != nil {
		return err
	}
	err = s.Store.WorkspaceWriteTransaction(ctx, ws, func(tx pgx.Tx) error {
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

// installRequestKey is the request key of management's attempt to install a policy generation
// for a key, so a pass that replays an attempt never queues a second one.
func installRequestKey(key, policy uuid.UUID, generation int64, attempt int32) uuid.UUID {
	raw, _ := stablejson.Marshal(struct {
		keyInstall
		Attempt int32 `json:"attempt"`
	}{keyInstall{Policy: policy, Generation: generation}, attempt})
	return uuid.NewSHA1(key, raw)
}

// queueKeyInstalls queues the installs re-mints are waiting for. An environment runs one operation
// at a time, so an install that can't start now is tried again a minute on. One that ended without
// its server reporting the generation, for example because the agent was offline until the
// command expired, is tried again under the key's next attempt, backing off.
func (s *Service) queueKeyInstalls(ctx context.Context) error {
	rows, err := s.Store.Queries.PendingKeyInstalls(ctx)
	if err != nil {
		return err
	}
	for len(rows) > 0 {
		if ctx.Err() != nil {
			return nil
		}
		n := 1
		for n < len(rows) && rows[n].KeyID == rows[0].KeyID {
			n++
		}
		if err = s.queueKeyInstall(ctx, rows[:n]); err != nil {
			return err
		}
		rows = rows[n:]
	}
	return nil
}

// queueKeyInstall queues one key's pending installs and records when to look at them again.
func (s *Service) queueKeyInstall(ctx context.Context, rows []db.PendingKeyInstallsRow) error {
	ws, key := rows[0].WorkspaceID, rows[0].KeyID
	return s.Store.WorkspaceWriteTransaction(ctx, ws, func(tx pgx.Tx) error {
		q := db.New(tx)
		retry := false
		for _, row := range rows {
			request := installRequestKey(key, row.PolicyID, row.Generation, row.InstallAttempts)
			state, err := q.InstallOperationState(ctx, db.InstallOperationStateParams{WorkspaceID: ws, RequestKey: request})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				err = s.queueInstall(ctx, tx, ws, row, request)
			case err != nil:
			case state == "failed" || state == "cancelled":
				retry = true
			}
			if err != nil {
				return err
			}
		}
		return q.RecordKeyInstallPass(ctx, db.RecordKeyInstallPassParams{WorkspaceID: ws, ID: key, Retry: retry})
	})
}

// queueInstall queues one install under request, in a savepoint, so an install refused for now,
// such as one whose environment is busy, leaves the rest of the pass's work standing.
func (s *Service) queueInstall(ctx context.Context, tx pgx.Tx, ws uuid.UUID, row db.PendingKeyInstallsRow, request uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = s.backupCommand(ctx, sp, ws, row.EnvironmentID, row.PolicyID, api.BackupCommandRequest{Action: "install", RequestKey: request})
	var refused *domain.Error
	if errors.As(err, &refused) {
		return sp.Rollback(ctx)
	} else if err != nil {
		return errors.Join(err, sp.Rollback(ctx))
	}
	return sp.Commit(ctx)
}
