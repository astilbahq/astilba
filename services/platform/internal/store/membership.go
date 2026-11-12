package store

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// EndAuthority withdraws what account holds or approved in ws on the strength of a role it is
// losing: from is its role before the change and to its role after, empty when it is removed. Call
// it in the transaction that changes the membership, under the workspace lock, so nothing it
// withdraws can be used after the change commits. p is the owner making the change, who is
// recorded as withdrawing each grant.
//
// A credential that acts as the account is revoked for good. A grant the account issued for the
// workspace is withdrawn, and a standing policy it approved is paused until a current member with
// the role it needs approves it again. None is handed to anyone else, so nothing ever runs on the
// approval of someone who did not give it. Re-promotion does not restore any of them.
//
// A changed authentication version needs no write here: every check of these grants compares it.
func EndAuthority(ctx context.Context, q *db.Queries, p Principal, ws, account uuid.UUID, from, to string) error {
	lostOwner := from == "owner" && to != "owner"
	lostWrite := domain.CanWrite(from) && !domain.CanWrite(to)
	if !lostOwner && !lostWrite {
		return nil
	}
	// Policies first: a pipeline report reading a policy's approval holds its row, so once these
	// have waited for it, the automatic deployment it adds is there for the block below to find.
	if lostOwner {
		sources, err := q.WithdrawGitSourceApprovals(ctx, db.WithdrawGitSourceApprovalsParams{WorkspaceID: ws, PolicyActor: account})
		if err = withdrawn(ctx, q, p, ws, "git.source.approval_withdraw", sources, err); err != nil {
			return err
		}
	}
	// An operator may still build automatically, so losing ownership pauses only the targets that
	// also deploy; losing write access pauses every one.
	targets, err := q.WithdrawBuildTargetApprovals(ctx, db.WithdrawBuildTargetApprovalsParams{WorkspaceID: ws, PolicyActor: account, LostWrite: lostWrite})
	if err = withdrawn(ctx, q, p, ws, "build.approval_withdraw", targets, err); err != nil {
		return err
	}
	if !lostOwner {
		return nil
	}
	deployments, err := q.BlockActorAutomaticDeployments(ctx, db.BlockActorAutomaticDeploymentsParams{WorkspaceID: ws, ActorID: account})
	if err = withdrawn(ctx, q, p, ws, "build.automatic_deployment_block", deployments, err); err != nil {
		return err
	}
	revoked, err := q.RevokeCreatorAutomationCredentials(ctx, db.RevokeCreatorAutomationCredentialsParams{WorkspaceID: ws, CreatorID: account})
	if err = withdrawn(ctx, q, p, ws, "automation.revoke", revoked, err); err != nil {
		return err
	}
	invitations, err := q.WithdrawIssuerInvitations(ctx, db.WithdrawIssuerInvitationsParams{WorkspaceID: ws, IssuerID: account})
	if err = withdrawn(ctx, q, p, ws, "invitation.revoke", invitations, err); err != nil {
		return err
	}
	codes, err := q.WithdrawIssuerEnrollmentCodes(ctx, db.WithdrawIssuerEnrollmentCodesParams{WorkspaceID: ws, IssuerID: account})
	if err = withdrawn(ctx, q, p, ws, "server.enrollment_withdraw", codes, err); err != nil {
		return err
	}
	previews, err := q.DisableActorPreviewPolicies(ctx, db.DisableActorPreviewPoliciesParams{WorkspaceID: ws, ActorID: account})
	if err = withdrawn(ctx, q, p, ws, "preview.policy_disable", previews, err); err != nil {
		return err
	}
	forks, err := q.WithdrawForkApprovals(ctx, db.WithdrawForkApprovalsParams{WorkspaceID: ws, ApprovedBy: &account})
	if err = withdrawn(ctx, q, p, ws, "preview.fork_approval_withdraw", forks, err); err != nil {
		return err
	}
	cleanups, err := q.WithdrawPreviewCleanupApprovals(ctx, db.WithdrawPreviewCleanupApprovalsParams{WorkspaceID: ws, CleanupActor: &account})
	return withdrawn(ctx, q, p, ws, "preview.cleanup_withdraw", cleanups, err)
}

func withdrawn(ctx context.Context, q *db.Queries, p Principal, ws uuid.UUID, action string, resources []uuid.UUID, err error) error {
	if err != nil {
		return err
	}
	for _, id := range resources {
		if err := Audit(ctx, q, p, ws, action, id); err != nil {
			return err
		}
	}
	return nil
}
