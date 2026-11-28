package alerts

import (
	"context"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// backupExposure raises backup_exposure for each destination whose storage keys a revoked server
// holds, as execution.BackupDestinations reports it exposed: one alert per destination, since the
// exposure belongs to its keys rather than to any one policy using them. It resolves once no
// revoked server holds them, or, for a managed destination, an hour after AWS confirmed each such
// key revoked. Once a destination's access can be replaced in place (PLAT-435), it
// also resolves when every policy using the destination is on a newer access generation than any
// revoked server was given.
func (s *Service) backupExposure(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	q := db.New(tx)
	exposed, err := q.ExposedBackupDestinations(ctx, ws)
	if err != nil {
		return err
	}
	firing, err := q.FiringAlertResources(ctx, db.FiringAlertResourcesParams{WorkspaceID: ws, Kind: string(api.BackupExposure)})
	if err != nil {
		return err
	}
	for _, destination := range exposed {
		if err = s.condition(ctx, tx, ws, api.BackupExposure, target{resource: destination}, bad); err != nil {
			return err
		}
	}
	for _, destination := range firing {
		if !slices.Contains(exposed, destination) {
			if err = s.condition(ctx, tx, ws, api.BackupExposure, target{resource: destination}, good); err != nil {
				return err
			}
		}
	}
	return nil
}

// backupAccount raises backup_account for each storage account whose latest check failed, or found
// a user or access key management didn't make, until a check finds none.
func (s *Service) backupAccount(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	q := db.New(tx)
	alerting, err := q.AlertingBackupStorageAccounts(ctx, ws)
	if err != nil {
		return err
	}
	firing, err := q.FiringAlertResources(ctx, db.FiringAlertResourcesParams{WorkspaceID: ws, Kind: string(api.BackupAccount)})
	if err != nil {
		return err
	}
	for _, account := range alerting {
		if err = s.condition(ctx, tx, ws, api.BackupAccount, target{resource: account}, bad); err != nil {
			return err
		}
	}
	for _, account := range firing {
		if !slices.Contains(alerting, account) {
			if err = s.condition(ctx, tx, ws, api.BackupAccount, target{resource: account}, good); err != nil {
				return err
			}
		}
	}
	return nil
}

// peerUserChanges is how many backup_peer_user alerts a sweep raises or resolves for a workspace.
const peerUserChanges = 200

// backupPeerUsers raises backup_peer_user for each user an account check found tagged with one of
// the account's peers, until the owner acknowledges it by name. A leaked admin key can make a user
// with a peer's tag, which the peer's own check doesn't see once that installation no longer
// checks the account, so each one is shown here once, as its own alert, however earlier ones were
// handled. Its resource is the user's identity: an acknowledgement, or the user being gone,
// establishes recovery, and a user made again under the name, or given another access key,
// raises it again.
func (s *Service) backupPeerUsers(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	q := db.New(tx)
	pending, err := q.UnacknowledgedPeerBackupStorageUsers(ctx, ws)
	if err != nil {
		return err
	}
	firing, err := q.FiringAlertResources(ctx, db.FiringAlertResourcesParams{WorkspaceID: ws, Kind: string(api.BackupPeerUser)})
	if err != nil {
		return err
	}
	// A leaked admin key can make up to 1000 users an account, so each is visited once, only a
	// change is written, and a sweep makes at most peerUserChanges of them within its time budget.
	// The next sweep makes the rest, since alerts it already raised aren't changes.
	budget := peerUserChanges
	open := make(map[uuid.UUID]bool, len(firing))
	for _, user := range firing {
		open[user] = true
	}
	for _, user := range pending {
		if open[user] {
			delete(open, user)
			continue
		}
		if budget--; budget < 0 {
			return nil
		}
		if err = s.condition(ctx, tx, ws, api.BackupPeerUser, target{resource: user}, bad); err != nil {
			return err
		}
	}
	for _, user := range firing {
		if !open[user] {
			continue
		}
		if budget--; budget < 0 {
			return nil
		}
		if err = s.condition(ctx, tx, ws, api.BackupPeerUser, target{resource: user}, good); err != nil {
			return err
		}
	}
	return nil
}
