package store

import (
	"context"
	"fmt"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/recoveryrows"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// rewrapFailed names the row, because rotation stops until an operator deals with it and
// the keyring's own error is deliberately the same for every cause.
func rewrapFailed(table string, workspace, id uuid.UUID, err error) error {
	return fmt.Errorf("cannot rewrap %s row %s in workspace %s: %w", table, id, workspace, err)
}

// Rewrap moves every stored value, and the installation's canaries, onto the active key, for an
// operator, as one recorded run. It wraps each value's data key again and keeps the data, and it
// never touches cross-host checkpoints, which hosts hold copies of by digest.
func (s *Store) Rewrap(ctx context.Context, operator, reason string) (err error) {
	rotation, err := s.Broker.BeginRotation(ctx, operator, reason)
	if err != nil {
		return err
	}
	defer func() {
		outcome := secretbroker.Used
		if err != nil {
			outcome = secretbroker.Failed
		}
		rotation.Close(ctx, outcome)
	}()
	if err = s.rewrapValues(ctx, rotation); err != nil {
		return err
	}
	if err = rotation.RewrapCanaries(ctx); err != nil {
		return err
	}
	return rotation.RewrapWrappings(ctx)
}

func (s *Store) rewrapValues(ctx context.Context, rotation *secretbroker.Rotation) error {
	return s.TransactionTx(ctx, func(tx pgx.Tx, q *db.Queries) error {
		if err := q.LockBootstrap(ctx); err != nil {
			return err
		}
		// Recovery's record of rows it restored moves to their content form before their values'
		// keys move, in the same transaction, so a checkpoint from before the rewrap still merges.
		if _, err := recoveryrows.RekeyAll(ctx, tx); err != nil {
			return err
		}
		versions, err := q.AllEncryptedVersions(ctx)
		if err != nil {
			return err
		}
		for _, v := range versions {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: v.ID}, v.EncryptedValue)
			if err != nil {
				return rewrapFailed("secret_versions", v.WorkspaceID, v.ID, err)
			}
			if err := q.RewrapSecretVersion(ctx, db.RewrapSecretVersionParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedValue: blob}); err != nil {
				return err
			}
		}

		git, err := q.AllGitAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range git {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.Git, ID: v.ID}, v.EncryptedAccess)
			if err != nil {
				return rewrapFailed("git_connections", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapGitAccess(ctx, db.RewrapGitAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		authorizations, err := q.AllGitHubAuthorizations(ctx)
		if err != nil {
			return err
		}
		for _, v := range authorizations {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.GitHubAuthorization, ID: v.ID}, v.EncryptedAccess)
			if err != nil {
				return rewrapFailed("github_app_authorizations", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapGitHubAuthorization(ctx, db.RewrapGitHubAuthorizationParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		registries, err := q.AllRegistryAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range registries {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.Registry, ID: v.ID}, v.EncryptedAccess)
			if err != nil {
				return rewrapFailed("registries", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapRegistryAccess(ctx, db.RewrapRegistryAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		build, err := q.AllBuildSecrets(ctx)
		if err != nil {
			return err
		}
		for _, v := range build {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.BuildSecret, ID: v.ID}, v.EncryptedValue)
			if err != nil {
				return rewrapFailed("build_secret_versions", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapBuildSecret(ctx, db.RewrapBuildSecretParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedValue: blob}); err != nil {
				return err
			}
		}

		rowsBackupDestination, err := q.AllBackupDestinationAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range rowsBackupDestination {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.BackupDestination, ID: v.ID}, v.EncryptedAccess)
			if err != nil {
				return rewrapFailed("backup_destinations", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapBackupDestinationAccess(ctx, db.RewrapBackupDestinationAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}

		rowsBackupPolicy, err := q.AllBackupPolicyAccess(ctx)
		if err != nil {
			return err
		}
		for _, v := range rowsBackupPolicy {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.BackupPolicy, ID: v.ID}, v.EncryptedAccess)
			if err != nil {
				return rewrapFailed("backup_policies", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapBackupPolicyAccess(ctx, db.RewrapBackupPolicyAccessParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedAccess: blob}); err != nil {
				return err
			}
		}
		storageAccounts, err := q.AllBackupStorageAccountCredentials(ctx)
		if err != nil {
			return err
		}
		for _, v := range storageAccounts {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.BackupStorageAccount, ID: v.ID}, v.EncryptedCredential)
			if err != nil {
				return rewrapFailed("backup_storage_accounts", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapBackupStorageAccountCredential(ctx, db.RewrapBackupStorageAccountCredentialParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedCredential: blob}); err != nil {
				return err
			}
		}
		storageKeys, err := q.AllBackupStorageKeySecrets(ctx)
		if err != nil {
			return err
		}
		for _, v := range storageKeys {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.BackupStorageKey, ID: v.ID}, v.EncryptedSecret)
			if err != nil {
				return rewrapFailed("backup_storage_keys", v.WorkspaceID, v.ID, err)
			}
			if err = q.RewrapBackupStorageKeySecret(ctx, db.RewrapBackupStorageKeySecretParams{WorkspaceID: v.WorkspaceID, ID: v.ID, EncryptedSecret: blob}); err != nil {
				return err
			}
		}
		hooks, err := q.AllAlertWebhooks(ctx)
		if err != nil {
			return err
		}
		for _, v := range hooks {
			blob, err := rotation.Rewrap(v.WorkspaceID, secretbroker.Item{Kind: secretbroker.AlertWebhook, ID: v.WorkspaceID}, v.EncryptedWebhook)
			if err != nil {
				return rewrapFailed("alert_settings", v.WorkspaceID, v.WorkspaceID, err)
			}
			if err = q.RewrapAlertWebhook(ctx, db.RewrapAlertWebhookParams{WorkspaceID: v.WorkspaceID, EncryptedWebhook: blob}); err != nil {
				return err
			}
		}
		accounts, err := q.AllTOTPSecrets(ctx)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			active, pending := a.TotpSecret, a.TotpPending
			if active != nil {
				active, err = rotation.Rewrap(uuid.Nil, secretbroker.Item{Kind: secretbroker.TOTP, ID: a.ID}, active)
				if err != nil {
					return rewrapFailed("accounts", uuid.Nil, a.ID, err)
				}
			}
			if pending != nil {
				pending, err = rotation.Rewrap(uuid.Nil, secretbroker.Item{Kind: secretbroker.TOTPPending, ID: a.ID}, pending)
				if err != nil {
					return rewrapFailed("accounts", uuid.Nil, a.ID, err)
				}
			}
			if err = q.RewrapTOTP(ctx, db.RewrapTOTPParams{ID: a.ID, TotpSecret: active, TotpPending: pending}); err != nil {
				return err
			}
		}
		return rotation.ValuesRewrapped(ctx, q)
	})
}
