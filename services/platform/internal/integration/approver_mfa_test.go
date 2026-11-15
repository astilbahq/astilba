package integration

import (
	"context"
	"maps"
	"slices"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// Requiring two-factor authentication stops every standing approval given by a member who hasn't
// set it up, as it stops their own writes: Git sources, build targets, pending automatic
// deployments, preview policies, fork approvals, cleanup requests, invitations and enrollment
// codes. Owners see beforehand what it would stop. Turning the requirement off lets them act
// again; setting up two-factor authentication changes the approver's credentials, so it ends
// them instead, and the approver approves again. Queued source submissions, automation
// credentials and terminal sessions have tests of their own.
func TestTwoFactorRequirementStopsStandingApprovals(t *testing.T) {
	p := newPolicyFixture(t)
	ctx := context.Background()
	q := db.New(p.f.st.Pool)
	prefix := "/api/v1/workspaces/" + p.ws.String()

	operator, _ := secondOwner(t, p.f, p.ws, "operator@example.test")
	if _, err := p.f.st.Pool.Exec(ctx, "UPDATE memberships SET role='operator' WHERE workspace_id=$1 AND account_id=(SELECT id FROM accounts WHERE email='operator@example.test')", p.ws); err != nil {
		t.Fatal(err)
	}
	if started := p.push(t); !started[p.deployer] {
		t.Fatal("no build to deploy")
	}
	var run uuid.UUID
	if err := p.f.st.Pool.QueryRow(ctx, "SELECT id FROM pipeline_runs WHERE workspace_id=$1 AND application_id=$2 ORDER BY created_at DESC LIMIT 1", p.ws, p.deployer).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO pipeline_auto_deployments(id,workspace_id,environment_id,revision_id,actor_id,actor_auth_version)
 SELECT r.id,r.workspace_id,r.environment_id,r.revision_id,$2,1 FROM pipeline_runs r WHERE r.id=$1`, run, p.approverID); err != nil {
		t.Fatal(err)
	}

	// A preview host, and a preview policy, fork approval and cleanup request the approver gave.
	var project, env, revision uuid.UUID
	if err := p.f.st.Pool.QueryRow(ctx, "SELECT e.project_id,e.id,e.draft_revision_id FROM environments e JOIN build_targets b ON b.workspace_id=e.workspace_id AND b.environment_id=e.id WHERE b.application_id=$1", p.builder).Scan(&project, &env, &revision); err != nil {
		t.Fatal(err)
	}
	host, policy, request := uuid.New(), uuid.New(), uuid.New()
	if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO servers(id,workspace_id,name,role,preview_only,controller_generation,capabilities) VALUES($1,$2,'Previews','combined',true,1,'{"previews":true}')`, host, p.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,enabled,configuration)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,1,true,'{}')`, policy, p.ws, project, env, revision, p.builder, p.connection, host, p.approverID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO preview_requests(id,workspace_id,policy_id,number,head,fork,approved_head,approved_by,approved_auth_version,cleanup_requested,cleanup_actor,cleanup_auth_version,cleanup_controller,next_check_at)
 VALUES($1,$2,$3,7,'abc',true,'abc',$4,1,true,$4,1,1,now()+interval '1 day')`, request, p.ws, policy, p.approverID); err != nil {
		t.Fatal(err)
	}

	// An invitation and an enrollment code the approver issued.
	p.approver.call("POST", prefix+"/invitations", map[string]string{"email": "invitee@example.test", "role": "viewer"}, 201, nil)
	code, in, _ := enrollment(t, p.f, p.approver, p.ws, "deployment")

	// held reads each approval through what decides whether it acts.
	held := func() map[string]bool {
		t.Helper()
		out := map[string]bool{}
		out["gitSource"] = p.source(t, p.owner).Approval == api.Current
		targets := p.targets(t, p.owner)
		out["builder"] = deref(targets[p.builder].Approval) == api.Current
		out["deployer"] = deref(targets[p.deployer].Approval) == api.Current
		var err error
		if run != uuid.Nil {
			if out["automaticDeployment"], err = q.AutomaticDeploymentApproved(ctx, db.AutomaticDeploymentApprovedParams{WorkspaceID: p.ws, ID: run}); err != nil {
				t.Fatal(err)
			}
		}
		// Preview policies and cleanup requests both ask PreviewApproverHolds, with their own approver.
		if out["previewPolicy"], err = q.PreviewApproverHolds(ctx, db.PreviewApproverHoldsParams{WorkspaceID: p.ws, ActorID: p.approverID, AuthVersion: 1, ServerID: host, ControllerGeneration: 1}); err != nil {
			t.Fatal(err)
		}
		fork, err := q.PreviewForkApproval(ctx, db.PreviewForkApprovalParams{WorkspaceID: p.ws, ID: request})
		if err != nil {
			t.Fatal(err)
		}
		out["previewApproval"] = fork.Head == "abc"
		var invitations []api.Invitation
		p.owner.call("GET", prefix+"/invitations", nil, 200, &invitations)
		out["invitation"] = slices.ContainsFunc(invitations, func(i api.Invitation) bool { return i.Email == "invitee@example.test" })
		var enrollable bool
		if err = p.f.st.Pool.QueryRow(ctx, "SELECT policy_approver_holds(workspace_id,enrollment_issuer_id,enrollment_issuer_auth_version,true) FROM servers WHERE id=$1", code.Server.Id).Scan(&enrollable); err != nil {
			t.Fatal(err)
		}
		out["enrollmentCode"] = enrollable
		return out
	}
	want := func(when string, holding bool) {
		t.Helper()
		for kind, got := range held() {
			if got != holding {
				t.Fatalf("%s: %s holds %v, want %v", when, kind, got, holding)
			}
		}
	}
	listed := func() map[string]int {
		t.Helper()
		var v api.MfaRequirement
		p.owner.call("GET", prefix+"/mfa-requirement", nil, 200, &v)
		out := map[string]int{}
		for _, a := range v.Approvals {
			if a.ApproverId != p.approverID || a.ApproverEmail != "approver@example.test" {
				t.Fatalf("listed an approval of %s", a.ApproverEmail)
			}
			out[string(a.Kind)]++
		}
		return out
	}
	requireMFA := func(on bool) {
		t.Helper()
		if _, err := p.f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=$2 WHERE id=$1", p.ws, on); err != nil {
			t.Fatal(err)
		}
	}

	want("before the requirement", true)
	// Owners see what requiring two-factor authentication would stop and whose it is; others don't.
	wantListed := map[string]int{"gitSource": 1, "buildTarget": 2, "automaticDeployment": 1, "previewPolicy": 1, "previewApproval": 1, "previewCleanup": 1, "invitation": 1, "enrollmentCode": 1}
	if got := listed(); !maps.Equal(got, wantListed) {
		t.Fatalf("listed %v, want %v", got, wantListed)
	}
	operator.call("GET", prefix+"/mfa-requirement", nil, 403, nil)

	requireMFA(true)
	want("while required", false)
	// Only owners, who see which members have set up two-factor authentication, learn why.
	p.want(t, p.owner, api.ApproverMfa, api.ApproverMfa, api.ApproverMfa)
	p.want(t, operator, api.ApprovalRequired, api.ApprovalRequired, api.ApprovalRequired)
	if started := p.push(t); len(started) != 0 {
		t.Fatal("a target built on the approval of a member without two-factor authentication", started)
	}
	agentRequest(t, p.f.server.Client(), p.f.server.URL, "/agent/v1/enroll", "", in, 401, nil)
	if got := listed(); len(got) != 0 {
		t.Fatal("listed approvals that have already stopped", got)
	}

	// The requirement is the workspace's policy, not a judgement of the approver: dropping it lets
	// their approvals act again.
	requireMFA(false)
	want("after the requirement is dropped", true)
	// A pending automatic deployment holds its environment, so it goes before builds are pushed.
	if _, err := p.f.st.Pool.Exec(ctx, "DELETE FROM pipeline_auto_deployments WHERE id=$1", run); err != nil {
		t.Fatal(err)
	}
	run = uuid.Nil
	if started := p.push(t); !started[p.builder] || !started[p.deployer] {
		t.Fatal("approved targets did not resume", started)
	}

	// Setting up two-factor authentication changes the approver's credentials, which ends what
	// they approved without it rather than resuming it.
	requireMFA(true)
	enableMFA(t, p.approver)
	want("after the approver sets up two-factor authentication", false)
	p.want(t, p.owner, api.ApproverCredentials, api.ApproverCredentials, api.ApproverCredentials)
	// They approve again, now with it, and their approvals act.
	p.approveSource(t, p.approver, 204)
	p.approveTarget(t, p.approver, p.builder, 204)
	p.approveTarget(t, p.approver, p.deployer, 204)
	p.want(t, p.owner, api.Current, api.Current, api.Current)
	if started := p.push(t); !started[p.builder] || !started[p.deployer] {
		t.Fatal("approvals given with two-factor authentication did not run", started)
	}
}
