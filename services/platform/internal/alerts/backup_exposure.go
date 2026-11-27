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
