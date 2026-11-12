package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// retireAction is the visual payload action that removes a retired environment's unit from its
// server. docs/environment-retirement.md describes what it removes and keeps.
const retireAction = "retire"

func retirementCommand(c api.ExecutionCommand) bool {
	var p deployment.Payload
	return c.Kind == "visual" && c.Visual != nil && json.Unmarshal(*c.Visual, &p) == nil && p.Action == retireAction
}

// requireActive refuses a change to an environment that is retiring or retired. Callers read the
// state under the workspace lock, which retirement and restore also hold.
func requireActive(state string) error {
	if state != "active" {
		return domain.EnvironmentInactive
	}
	return nil
}

// RetireEnvironment needs the owner's recent confirmation and the environment's exact current
// name. It is a recovery action: retiring only reduces what the workspace runs.
func (s *Service) RetireEnvironment(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, confirmName string) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: true}, "environment.retire", env, func(tx pgx.Tx) error {
		return s.retire(ctx, tx, p, ws, pid, env, confirmName)
	})
}

func (s *Service) retire(ctx context.Context, tx pgx.Tx, p store.Principal, ws, pid, env uuid.UUID, confirmName string) error {
	q := db.New(tx)
	e, err := q.LockRetirementEnvironment(ctx, db.LockRetirementEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
	if err != nil {
		return err
	}
	if e.Name != confirmName {
		return domain.Invalid("confirmName")
	}
	switch e.State {
	case "active":
	case "retiring":
		// Asking again re-queues a retirement only once its operation has ended without success.
		state, err := q.RetirementOperationState(ctx, db.RetirementOperationStateParams{WorkspaceID: ws, EnvironmentID: env, ID: *e.RetirementOperationID})
		if err != nil {
			return err
		}
		if state != "failed" && state != "cancelled" {
			return domain.RetirementRefused(domain.RetirementActiveOperation)
		}
	default:
		return domain.RetirementRefused(domain.RetirementState)
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
	case holds.CutoverHold:
		return domain.RetirementRefused(domain.RetirementCutover)
	case holds.CrossHost:
		return domain.RetirementRefused(domain.RetirementCrossHost)
	}
	next := db.SetEnvironmentRetirementParams{State: "retired", RetiredBy: p.Account.ID, WorkspaceID: ws, ID: env}
	var plan deployment.Plan
	contact := e.ServerID != nil
	if contact {
		if plan, next.RetiredWithoutHost, err = retirementPlan(ctx, tx, ws, env, e); err != nil {
			return err
		}
		server, err := q.RetirementServer(ctx, db.RetirementServerParams{WorkspaceID: ws, ID: *e.ServerID})
		if err != nil {
			return err
		}
		if server.Revoked {
			// Nothing can be delivered to a revoked server, and revocation never stopped what it runs.
			// The controller keeps what it last knew about that runtime, because nothing confirmed a change.
			contact, next.RetiredWithoutHost = false, true
		} else {
			var capabilities api.AgentCapabilities
			switch {
			case server.AwaitingTransfer:
				return domain.RetirementRefused(domain.RetirementServerTransfer)
			case json.Unmarshal(server.Capabilities, &capabilities) != nil || capabilities.Retirement == nil || !*capabilities.Retirement:
				return domain.RetirementRefused(domain.RetirementAgentUpdate)
			case holds.ServerBusy:
				return domain.RetirementRefused(domain.RetirementServerBusy)
			}
		}
	}
	if err = s.retireDependents(ctx, tx, p, ws, env); err != nil {
		return err
	}
	if contact {
		op, err := s.insertVisual(ctx, tx, ws, uuid.New(), deployment.Payload{Action: retireAction, Plan: plan})
		if err != nil {
			return err
		}
		next.State, next.RetirementOperationID = "retiring", &op.Id
	}
	return q.SetEnvironmentRetirement(ctx, next)
}

// retirementPlan is what the retirement command removes from the server of the environment's unit:
// the plan the environment last applied there, which names everything it put on that server. A unit
// whose first deployment never succeeded gets an identity-only plan: the agent finds what such an
// attempt left by its ownership labels, not by the plan's services.
//
// A primary replacement moves the unit to its new server when it is applied, but the runtime keeps
// naming the plan on the revoked server until the replacement's deployment succeeds. If it failed,
// the new server gets the identity-only plan for whatever the attempt left there, and the revoked
// server, which nothing can reach, is recorded as not contacted.
func retirementPlan(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, e db.LockRetirementEnvironmentRow) (plan deployment.Plan, withoutHost bool, err error) {
	q := db.New(tx)
	id, err := q.EnvironmentRuntimePlan(ctx, db.EnvironmentRuntimePlanParams{WorkspaceID: ws, EnvironmentID: env})
	switch {
	case err == nil:
		if plan, err = readPlan(ctx, tx, ws, env, id); err != nil {
			return plan, false, err
		}
		if plan.Distributed != nil || plan.Local != nil || plan.Specification.Placement != nil {
			return plan, false, domain.RetirementRefused(domain.RetirementCrossHost)
		}
		if plan.ServerID == *e.ServerID && plan.UnitID == e.UnitID {
			break
		}
		applied, err := q.RetirementServer(ctx, db.RetirementServerParams{WorkspaceID: ws, ID: plan.ServerID})
		if err != nil {
			return plan, false, err
		}
		// Only an unfinished replacement leaves the runtime on another server, and only off a revoked
		// one. A server renewed since then may still run that plan, beside whatever the attempt left on
		// the unit's new server: that is two servers, which one command cannot retire. Finishing the
		// replacement or revoking the former server again leaves one.
		if !applied.Revoked {
			return plan, false, domain.RetirementRefused(domain.RetirementReplacement)
		}
		plan, withoutHost = identityPlan(env, e), true
	case errors.Is(err, pgx.ErrNoRows):
		plan = identityPlan(env, e)
	default:
		return plan, false, err
	}
	if plan.Validate(false) != nil {
		return plan, false, domain.Invalid("deployment.plan")
	}
	return plan, withoutHost, nil
}

// identityPlan names only the environment's unit on its server, with nothing to deploy.
func identityPlan(env uuid.UUID, e db.LockRetirementEnvironmentRow) deployment.Plan {
	revision := e.DraftRevisionID
	if e.AppliedRevisionID != nil {
		revision = *e.AppliedRevisionID
	}
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}
	return deployment.Plan{ID: uuid.New(), ServerID: *e.ServerID, EnvironmentID: env, UnitID: e.UnitID, RevisionID: revision, Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{}, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}.WithHash()
}

// retireDependents stops everything that would act for the environment on its own: automation
// credentials, previews created from it, terminal sessions and backup schedules. Restoring the
// environment turns none of them back on. Alerts end on the next sweep, which stops monitoring
// environments that are not active.
func (s *Service) retireDependents(ctx context.Context, tx pgx.Tx, p store.Principal, ws, env uuid.UUID) error {
	q := db.New(tx)
	revoked, err := q.RevokeEnvironmentAutomationCredentials(ctx, db.RevokeEnvironmentAutomationCredentialsParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil {
		return err
	}
	for _, id := range revoked {
		if err = store.Audit(ctx, q, p, ws, "automation.revoke", id); err != nil {
			return err
		}
	}
	policies, err := q.DisableEnvironmentPreviewPolicy(ctx, db.DisableEnvironmentPreviewPolicyParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if err = s.cancelPreviewWork(ctx, tx, ws, policy.ID); err != nil {
			return err
		}
		// The retiring owner's authority, as when an owner asks to clean up one preview.
		requests, err := q.RequestPolicyPreviewCleanup(ctx, db.RequestPolicyPreviewCleanupParams{ActorID: p.Account.ID, AuthVersion: p.Account.AuthVersion, WorkspaceID: ws, PolicyID: policy.ID, ServerID: policy.ServerID})
		if err != nil {
			return err
		}
		for _, id := range requests {
			if err = store.Audit(ctx, q, p, ws, "preview.cleanup", id); err != nil {
				return err
			}
			if err = s.enqueuePreview(ctx, tx, ws, previewArgs{Request: id}); err != nil {
				return err
			}
		}
	}
	if err = q.DisableEnvironmentTroubleshootingPolicy(ctx, db.DisableEnvironmentTroubleshootingPolicyParams{UpdatedBy: p.Account.ID, WorkspaceID: ws, EnvironmentID: env}); err != nil {
		return err
	}
	ended, err := q.CloseEnvironmentTroubleshootingSessions(ctx, db.CloseEnvironmentTroubleshootingSessionsParams{CloseReason: "environment retired", WorkspaceID: ws, EnvironmentID: env})
	if err != nil {
		return err
	}
	for _, id := range ended {
		if err = store.Audit(ctx, q, p, ws, "session.close", id); err != nil {
			return err
		}
	}
	// The retirement command removes their schedules from the host.
	return q.DisableEnvironmentBackupPolicies(ctx, db.DisableEnvironmentBackupPoliciesParams{WorkspaceID: ws, EnvironmentID: env})
}

// finalizeRetirement publishes a host's confirmed removal, from a report or a recovered checkpoint.
// The command's own identity, not anything the host says, decides what is released.
func (s *Service) finalizeRetirement(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	var p deployment.Payload
	if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != retireAction || p.Plan.Preview != nil || p.Digest() != c.PlanHash || p.Plan.ServerID != c.ServerId || p.Plan.EnvironmentID != c.EnvironmentId || p.Plan.UnitID != c.ResourceId || p.Plan.Validate(false) != nil {
		return domain.Forbidden
	}
	q := db.New(tx)
	known, err := q.OperationReportState(ctx, db.OperationReportStateParams{WorkspaceID: ws, ID: c.Id})
	if err != nil {
		return err
	}
	if known.ReportSequence == settledReportSequence {
		return nil
	}
	if err = recordRuntimeOutcome(ctx, tx, ws, c.EnvironmentId, c.Id, portability.RuntimeOutcome{Kind: "removed"}); err != nil {
		return err
	}
	if err = q.DeleteEnvironmentRuntime(ctx, db.DeleteEnvironmentRuntimeParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId}); err != nil {
		return err
	}
	if err = q.ClearEnvironmentApplied(ctx, db.ClearEnvironmentAppliedParams{WorkspaceID: ws, ID: c.EnvironmentId}); err != nil {
		return err
	}
	// Another environment can now serve these hostnames and ports.
	if err = q.ReleaseEnvironmentRouteClaims(ctx, db.ReleaseEnvironmentRouteClaimsParams{WorkspaceID: ws, EnvironmentID: c.EnvironmentId}); err != nil {
		return err
	}
	// A recovered database can predate the retirement; the host's removal stands either way.
	_, err = q.CompleteEnvironmentRetirement(ctx, db.CompleteEnvironmentRetirementParams{WorkspaceID: ws, ID: c.EnvironmentId, RetirementOperationID: c.Id})
	return err
}

// finalizeRemoval publishes a recovered removal by the command that made it.
func (s *Service) finalizeRemoval(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	if retirementCommand(c) {
		return s.finalizeRetirement(ctx, tx, ws, c)
	}
	return s.finalizePreviewCleanup(ctx, tx, ws, c)
}

// RestoreEnvironment makes a retired environment active again as configuration only. Nothing is
// deployed, and its credentials and policies stay off until an owner turns them back on.
func (s *Service) RestoreEnvironment(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, confirmName string) error {
	return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "environment.restore", env, func(tx pgx.Tx) error {
		q := db.New(tx)
		e, err := q.LockRetirementEnvironment(ctx, db.LockRetirementEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: env})
		if err != nil {
			return err
		}
		if e.Name != confirmName {
			return domain.Invalid("confirmName")
		}
		n, err := q.RestoreRetiredEnvironment(ctx, db.RestoreRetiredEnvironmentParams{WorkspaceID: ws, ID: env})
		if err == nil && n != 1 {
			err = domain.RetirementRefused(domain.RetirementState)
		}
		return err
	})
}

// routesAvailable names a hostname or port that another environment on the plan's server serves,
// before the review rather than at apply.
func routesAvailable(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, plan deployment.Plan) error {
	claims, err := deployment.Claims(plan)
	if err != nil {
		return domain.Invalid("routes")
	}
	for _, claim := range claims {
		owner, err := db.New(tx).RouteClaimOwner(ctx, db.RouteClaimOwnerParams{WorkspaceID: ws, ServerID: plan.ServerID, Claim: claim})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if owner != env {
			return domain.RouteClaimed(claim)
		}
	}
	return nil
}
