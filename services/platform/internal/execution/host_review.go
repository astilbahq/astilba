package execution

import (
	"context"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ApplyVisualPlan applies a reviewed plan. Most plans change only the environment's own
// services and need nothing more than write access. A plan that also changes its host needs
// an owner who approves the host changes it lists and confirmed their identity recently, in
// that order, so a confirmation is only asked for once the changes are approved. MFA is
// governed by the workspace's own requirement, as for any other write.
func (s *Service) ApplyVisualPlan(ctx context.Context, p store.Principal, ws, pid, env, id, key uuid.UUID, approveHostChanges bool) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true}, "deployment.apply", id, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, "SELECT role FROM memberships WHERE workspace_id=$1 AND account_id=$2", ws, p.Account.ID).Scan(&role); err != nil {
			return err
		}
		review, err := db.New(tx).VisualPlanHostReview(ctx, db.VisualPlanHostReviewParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
		if err != nil {
			return err
		}
		if review {
			// Automation cannot confirm an identity, so host changes stay with a person. They
			// can give root on the server, so they take the shorter recent window.
			if role != "owner" || p.Automation != nil {
				return domain.Forbidden
			}
			if !approveHostChanges {
				return domain.Invalid("approveHostChanges")
			}
			if !p.CredentialRecent() {
				return domain.RecentRequired
			}
		}
		out, err = s.applyVisualPlan(ctx, tx, ws, pid, env, id, key)
		return err
	})
	return out, err
}

// standingHostPermissions are the Compose host permissions the environment's applied deployment
// already has on this server, which an owner approved when it was applied. Published ports stand
// on that alone; an elevated service must also stand, see unapprovedElevatedServices.
func standingHostPermissions(applied deployment.Plan, server uuid.UUID) ([]string, error) {
	if applied.ID == uuid.Nil || applied.ServerID != server || applied.Specification.Compose == nil {
		return nil, nil
	}
	source, err := deployment.SourceProject(applied, nil)
	if err != nil {
		return nil, err
	}
	return source.Permissions, nil
}

// unapprovedElevatedServices names the elevated services of a resolved plan, or of the applied
// deployment it replaces, that do not stand as an owner approved them: the applied deployment
// ran each on the same server with the same definition and inputs, and it resolved to the same
// image. Automatic deployment makes the same check, so neither path carries an approval further
// than the other.
func unapprovedElevatedServices(applied, plan deployment.Plan) ([]string, error) {
	reviewed := domain.Specification{}
	if applied.ID != uuid.Nil && applied.ServerID == plan.ServerID {
		reviewed = applied.Specification
	}
	elevated, err := reviewElevatedServices(reviewed, plan.Specification)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for id, service := range elevated {
		if !service.Unchanged || !resolvedImageStands(planImages(applied), planImages(plan), id) {
			names = append(names, service.Name)
		}
	}
	slices.Sort(names)
	return names, nil
}
