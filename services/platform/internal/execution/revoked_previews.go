package execution

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const replacedPreviewNote = "This is a new preview: the one before it was released when its preview host was revoked."

const revokedPolicyNote = "Its preview host was revoked, so its previews were released and its fork approvals withdrawn. Save the policy with another preview host, or renew this one on a rebuilt machine with a fresh agent install."

// revokedPolicyConflict answers a save made from a policy loaded before its host's revocation.
var revokedPolicyConflict = &domain.Error{Code: "conflict", Message: "This policy changed because its preview host was revoked, which withdrew its fork approvals. Reload it before saving."}

const revokedPreviewNote = "Released because its preview host was revoked. That host can't confirm removal, so this preview's services, data and copies of the template's preview secrets may remain on it: rotate those secrets. To continue, save the policy with another preview host or renew this one on a rebuilt machine."

// releaseRevokedPreviews frees the slots of previews on a revoked reserved host. Their removal can
// never be confirmed, so waiting would hold the slots and the policy's host for good. Management's
// copies of their secrets have no further use: a request that comes back gets a new environment.
func releaseRevokedPreviews(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	q := db.New(tx)
	if err := q.WithdrawRevokedHostPreviewApprovals(ctx, db.WithdrawRevokedHostPreviewApprovalsParams{Note: revokedPolicyNote, WorkspaceID: ws, ServerID: server}); err != nil {
		return err
	}
	released, err := q.ReleaseRevokedHostPreviews(ctx, db.ReleaseRevokedHostPreviewsParams{Note: revokedPreviewNote, WorkspaceID: ws, ServerID: server})
	if err != nil {
		return err
	}
	for _, env := range released {
		if err = q.DeleteEnvironmentSecretVersions(ctx, db.DeleteEnvironmentSecretVersionsParams{WorkspaceID: ws, EnvironmentID: env}); err != nil {
			return err
		}
		if err = q.DeleteEnvironmentSecrets(ctx, db.DeleteEnvironmentSecretsParams{WorkspaceID: ws, EnvironmentID: env}); err != nil {
			return err
		}
	}
	return nil
}
