package execution

import (
	"context"
	"encoding/json/v2"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// purgeAction is the visual payload action that deletes a retired environment's retained data
// volumes from its server. docs/environment-retirement.md describes purging.
const purgeAction = "purge"

// lifecycleAction is the retire or purge action a visual command carries, or "".
func lifecycleAction(c api.ExecutionCommand) string {
	var p deployment.Payload
	if c.Kind != "visual" || c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil {
		return ""
	}
	if p.Action == retireAction || p.Action == purgeAction {
		return p.Action
	}
	return ""
}

// removalCommand checks a cleanup, retire or purge command's own identity, which alone decides
// what a host's confirmation changes, and says whether its report was already settled. Only a
// preview cleanup names a preview's plan.
func removalCommand(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand, action string) (deployment.Payload, bool, error) {
	var p deployment.Payload
	if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != action || (p.Plan.Preview != nil) != (action == "preview-cleanup") || p.Digest() != c.PlanHash || p.Plan.ServerID != c.ServerId || p.Plan.EnvironmentID != c.EnvironmentId || p.Plan.UnitID != c.ResourceId || p.Plan.Validate(false) != nil {
		return p, false, domain.Forbidden
	}
	known, err := db.New(tx).OperationReportState(ctx, db.OperationReportStateParams{WorkspaceID: ws, ID: c.Id})
	if err != nil {
		return p, false, err
	}
	return p, known.ReportSequence == settledReportSequence, nil
}

// lifecycleAdmits says whether an environment in state may get command: only the command that
// moves it to its next state, since every other operation is refused while it is not active.
func lifecycleAdmits(state string, c api.ExecutionCommand) bool {
	switch lifecycleAction(c) {
	case retireAction:
		return state == "active" || state == "retiring"
	case purgeAction:
		return state == "retired" || state == "purging"
	}
	return state == "active"
}

// PurgeEnvironment deletes a retired environment's retained data volumes and erases its secret
// values. It needs the owner's recent confirmation, the environment's exact current name and
// exactly the volumes the environment keeps, so it never deletes data the owner was not shown.
// Like retiring, it only reduces what the workspace runs and stores.
func (s *Service) PurgeEnvironment(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, confirmName string, volumes []uuid.UUID) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: true}, "environment.purge", env, func(tx pgx.Tx) error {
		return s.purge(ctx, tx, p, ws, pid, env, confirmName, volumes)
	})
}

func (s *Service) purge(ctx context.Context, tx pgx.Tx, p store.Principal, ws, pid, env uuid.UUID, confirmName string, volumes []uuid.UUID) error {
	q := db.New(tx)
	e, err := q.LockRetirementEnvironment(ctx, db.LockRetirementEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
	if err != nil {
		return err
	}
	if e.Name != confirmName {
		return domain.Invalid("confirmName")
	}
	switch e.State {
	case "retired":
	case "purging":
		// Asking again re-queues a purge only once its operation has ended without success.
		state, err := q.RetirementOperationState(ctx, db.RetirementOperationStateParams{WorkspaceID: ws, EnvironmentID: env, ID: *e.PurgeOperationID})
		if err != nil {
			return err
		}
		if state != "failed" && state != "cancelled" {
			return domain.RetirementRefused(domain.RetirementActiveOperation)
		}
	default:
		return domain.RetirementRefused(domain.RetirementState)
	}
	// The volumes it keeps on its unit's server, as its volume list shows them, which a purge names
	// exactly.
	retained, err := q.EnvironmentVolumes(ctx, db.EnvironmentVolumesParams{WorkspaceID: ws, EnvironmentID: env, UnitID: e.UnitID})
	if err != nil {
		return err
	}
	confirmed := slices.Clone(volumes)
	slices.SortFunc(confirmed, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(confirmed, retained) {
		return domain.RetirementRefused(domain.RetirementVolumes)
	}
	holds, err := q.RetirementHolds(ctx, db.RetirementHoldsParams{WorkspaceID: ws, EnvironmentID: env, ServerID: e.ServerID})
	if err != nil {
		return err
	}
	switch {
	case holds.ActiveOperation:
		return domain.RetirementRefused(domain.RetirementActiveOperation)
	case holds.UpgradeHold:
		return domain.RetirementRefused(domain.RetirementUpgrade)
	}
	// A preview template's allocated previews hold their own copies of its secret values, which only
	// their cleanup deletes, so a purge waits for all of them. This transaction holds the workspace write
	// lock that every change to a preview takes, and a template that is not active starts or revives
	// none, so none can appear between this check and the purge, or after it.
	previews, err := q.UndeletedTemplatePreviews(ctx, db.UndeletedTemplatePreviewsParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil {
		return err
	}
	if len(previews) > 0 {
		pending := make([]domain.PendingPreview, len(previews))
		for i, r := range previews {
			pending[i] = domain.PendingPreview{Number: r.Number, State: r.State, Note: r.Note}
		}
		return domain.PreviewsRemain(pending)
	}
	next := db.SetEnvironmentPurgeParams{State: "purged", PurgedBy: p.Account.ID, WorkspaceID: ws, ID: env}
	// Only volumes need the server. An environment that kept none is purged here at once.
	contact := e.ServerID != nil && len(retained) > 0
	if contact {
		revoked, err := lifecycleServer(ctx, q, ws, *e.ServerID, holds.ServerBusy, func(c api.AgentCapabilities) *bool { return c.Purge }, domain.RetirementPurgeAgent)
		if err != nil {
			return err
		}
		if revoked {
			// Nothing can be delivered to a revoked server. Its volumes stay listed as not deleted.
			contact, next.PurgedWithoutHost = false, true
		}
	}
	// Purging is one-way, so nothing can use the values again: they go now, whether or not the
	// server is reachable. Each version's row stays for the revisions that name it.
	if err = q.EraseEnvironmentSecretValues(ctx, db.EraseEnvironmentSecretValuesParams{WorkspaceID: ws, EnvironmentID: env}); err != nil {
		return err
	}
	if contact {
		plan := identityPlan(env, e)
		if plan.Validate(false) != nil {
			return domain.Invalid("deployment.plan")
		}
		op, err := s.insertVisual(ctx, tx, ws, uuid.New(), deployment.Payload{Action: purgeAction, Plan: plan, VolumeIDs: retained})
		if err != nil {
			return err
		}
		next.State, next.PurgeOperationID = "purging", &op.Id
	}
	return q.SetEnvironmentPurge(ctx, next)
}

// finalizePurge records a host's confirmed deletion of the volumes its purge command named. The
// command's own identity, not anything the host says, decides which volumes are marked deleted.
func (s *Service) finalizePurge(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	p, settled, err := removalCommand(ctx, tx, ws, c, purgeAction)
	if err != nil || settled {
		return err
	}
	q := db.New(tx)
	if err = q.MarkVolumesDeleted(ctx, db.MarkVolumesDeletedParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId, Ids: p.VolumeIDs}); err != nil {
		return err
	}
	_, err = q.CompleteEnvironmentPurge(ctx, db.CompleteEnvironmentPurgeParams{WorkspaceID: ws, ID: c.EnvironmentId, PurgeOperationID: c.Id})
	return err
}
