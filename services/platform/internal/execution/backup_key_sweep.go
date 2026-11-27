package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/sweep"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// The key sweep drives every minted key to the state its rows ask for. Revocation is desired
// state, not an event: a key is latched (revoking) when its holder is revoked, its recovery has
// ended, its mint failed or never finished, or a newer key has replaced it on a server that
// installed that one, and
// the sweep revokes latched keys at AWS until AWS confirms. It also starts re-mints for renewed
// servers' destinations. See docs/backups.md#managed-keys.
type keySweepWorker struct {
	river.WorkerDefaults[keySweepArgs]
	service *Service
}

func (w *keySweepWorker) Work(ctx context.Context, _ *river.Job[keySweepArgs]) error {
	return w.service.runKeySweep(ctx)
}

// keySweepPrincipal opens admin credentials for revocation, whatever the workspace's
// subscription state: revocation only takes access away.
var keySweepPrincipal = secretbroker.System("backup-keys")

func (s *Service) runKeySweep(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sweepPassBudget)
	defer cancel()
	// Each step runs whatever an earlier one hit, so nothing stops revocation. Latching runs
	// first, so the keys it latches are revoked in the same pass.
	return errors.Join(s.latchDerivedKeys(ctx), s.queueRemints(ctx), s.queueKeyInstalls(ctx), s.revokeDueKeys(ctx))
}

// latchDerivedKeys latches the keys whose rows say they are done with: keys still minting past
// their deadline, recovery keys whose operation has ended however it ended, and server keys
// replaced by a newer one that their holder has installed. Only a management recovery that merges
// two histories, each of which re-minted the destination, leaves two active server keys.
func (s *Service) latchDerivedKeys(ctx context.Context) error {
	if _, err := s.Store.Queries.LatchExpiredMintingKeys(ctx); err != nil {
		return err
	}
	if _, err := s.Store.Queries.LatchEndedRecoveryKeys(ctx); err != nil {
		return err
	}
	rows, err := s.Store.Queries.DestinationServerKeys(ctx)
	if err != nil {
		return err
	}
	type destination struct{ ws, id uuid.UUID }
	current := map[destination]db.DestinationServerKeysRow{}
	for _, row := range rows {
		d := destination{row.WorkspaceID, *row.DestinationID}
		newest, seen := current[d]
		if !seen {
			current[d] = row
			continue
		}
		installed, err := s.installedEverywhere(ctx, row.WorkspaceID, newest.Installs)
		if err != nil || !installed || ctx.Err() != nil {
			continue
		}
		if err = s.Store.WorkspaceWriteTransaction(ctx, row.WorkspaceID, func(tx pgx.Tx) error {
			code := "superseded"
			_, err := db.New(tx).LatchBackupStorageKey(ctx, db.LatchBackupStorageKeyParams{WorkspaceID: row.WorkspaceID, ID: row.ID, LastError: fixedText(code)})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// installedEverywhere says whether the holder reports every policy generation whose install
// carries a key, so it no longer uses an older one. A policy that is gone uses no key.
func (s *Service) installedEverywhere(ctx context.Context, ws uuid.UUID, raw []byte) (bool, error) {
	var installs []keyInstall
	if err := json.Unmarshal(raw, &installs); err != nil {
		return false, err
	}
	if len(installs) == 0 {
		return true, nil
	}
	ids := make([]uuid.UUID, 0, len(installs))
	for _, i := range installs {
		ids = append(ids, i.Policy)
	}
	rows, err := s.Store.Queries.PolicyReportedGenerations(ctx, db.PolicyReportedGenerationsParams{WorkspaceID: ws, Ids: ids})
	if err != nil {
		return false, err
	}
	reported := map[uuid.UUID]int64{}
	for _, r := range rows {
		reported[r.ID] = r.Reported
	}
	for _, i := range installs {
		if g, ok := reported[i.Policy]; ok && g < i.Generation {
			return false, nil
		}
	}
	return true, nil
}

// queueRemints starts a re-mint job for each destination whose renewed server has no key.
func (s *Service) queueRemints(ctx context.Context) error {
	for range 20 {
		if ctx.Err() != nil {
			return nil
		}
		// Claiming moves the destination's next attempt on, so a job that is never inserted is
		// claimed again later.
		claimed, err := s.Store.Queries.ClaimDueBackupRemint(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if _, err = s.Queue.Insert(ctx, remintArgs{Workspace: claimed.WorkspaceID, Destination: claimed.ID}, &remintOpts); err != nil {
			return err
		}
	}
	return nil
}

// revokeDueKeys revokes latched keys until none is due or the pass ends. A workspace whose keys
// have used one item's share sits out the rest of the pass, so one failing account can't hold up
// another workspace's revocations.
func (s *Service) revokeDueKeys(ctx context.Context) error {
	spent := map[uuid.UUID]time.Duration{}
	stalled := []uuid.UUID{}
	for {
		k, err := s.Store.Queries.ClaimDueBackupStorageKey(ctx, stalled)
		if errors.Is(err, pgx.ErrNoRows) || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		began := time.Now()
		_ = sweep.Item(ctx, "backup_key_revocation", k.WorkspaceID, sweepItemLimit-spent[k.WorkspaceID], func(item context.Context) error {
			return s.revokeKey(item, k)
		})
		spent[k.WorkspaceID] += time.Since(began)
		if spent[k.WorkspaceID] >= sweepItemLimit {
			stalled = append(stalled, k.WorkspaceID)
		}
	}
}

// revokeKey revokes one latched key at AWS and records the outcome. A missing user counts as
// revoked only once the admin credential is confirmed to answer for the account the key was
// minted in: another account's credential would find every user missing.
func (s *Service) revokeKey(ctx context.Context, k db.ClaimDueBackupStorageKeyRow) error {
	err := s.revokeAtAWS(ctx, k)
	if ctx.Err() != nil && err != nil {
		// The pass ran out of time; the key keeps its claim's short delay and is tried again.
		return nil
	}
	// What AWS answered is recorded even once the pass has run out of time.
	work := context.WithoutCancel(ctx)
	return s.Store.WorkspaceWriteTransaction(work, k.WorkspaceID, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err != nil {
			return q.RecordBackupStorageKeyFailure(work, db.RecordBackupStorageKeyFailureParams{WorkspaceID: k.WorkspaceID, ID: k.ID, LastError: fixedText(errorCode(err))})
		}
		destination, err := q.RecordBackupStorageKeyRevoked(work, db.RecordBackupStorageKeyRevokedParams{WorkspaceID: k.WorkspaceID, ID: k.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil || destination == nil {
			return err
		}
		// A failed create's placeholder goes once nothing of it is left to revoke.
		_, err = q.DeleteBackupPlaceholder(work, db.DeleteBackupPlaceholderParams{WorkspaceID: k.WorkspaceID, DestinationID: *destination})
		return err
	})
}

func (s *Service) revokeAtAWS(ctx context.Context, k db.ClaimDueBackupStorageKeyRow) error {
	account, err := s.Store.Queries.BackupStorageAccount(ctx, db.BackupStorageAccountParams{WorkspaceID: k.WorkspaceID, ID: k.AccountID})
	if err != nil {
		return err
	}
	admin, err := s.adminCredential(ctx, keySweepPrincipal, k.WorkspaceID, account.ID, account.EncryptedCredential, "revoke")
	if err != nil {
		return err
	}
	defer clearCredential(&admin)
	id, err := s.AWS.Identify(ctx, admin)
	if err != nil {
		return err
	}
	if id.Account != account.AwsAccount {
		return errAccountMoved
	}
	err = s.AWS.Revoke(ctx, admin, awskeys.Path(k.WorkspaceID.String()), k.UserName)
	switch {
	case errors.Is(err, awskeys.ErrNotFound):
		return nil
	case errors.Is(err, awskeys.ErrUnlisted) && time.Since(k.CreatedAt) > unlistedAfter:
		// IAM refuses a missing user by name under a path-scoped admin policy. The user, if any,
		// was made after the key's row, by the mint, so by now it would be listed.
		return nil
	}
	return err
}

// unlistedAfter is how old a key's row must be before a user IAM refuses by name and doesn't list
// counts as gone: past the mint that may still be making the user, and a margin for ListUsers,
// which can lag a create.
const unlistedAfter = mintDeadline + 10*time.Minute
