package integration

import (
	"context"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// ownerGrants are what one owner issued: a credential that acts as them, and an invitation and
// an enrollment code that act for the workspace on their say-so.
type ownerGrants struct {
	automation string
	invitation string
	enrollment api.AgentEnrollment
}

func issueOwnerGrants(t *testing.T, f *fixture, b *browser, ws uuid.UUID, path, email string) ownerGrants {
	t.Helper()
	g := ownerGrants{automation: *issueAutomation(t, b, path, domain.EnvironmentRead).Token}
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/invitations", map[string]string{"email": email, "role": "owner"}, 201, nil)
	g.invitation = tokenFromLinkMail(t, f, "/invitation")
	_, g.enrollment, _ = enrollment(t, f, b, ws, "deployment")
	return g
}

// refused checks that none of the grants works any more, and leaves each unspent.
func (g ownerGrants) refused(t *testing.T, f *fixture, path string) {
	t.Helper()
	bearerCall(t, f, g.automation, "GET", "/api/v1/automation/context", nil, 401, nil)
	guest := f.browser(t)
	guest.call("POST", "/api/v1/auth/invitations/validate", map[string]string{"token": g.invitation}, 400, nil)
	guest.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": g.invitation, "name": "Invited owner", "password": f.password}, 400, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", g.enrollment, 401, nil)
}

func (g ownerGrants) work(t *testing.T, f *fixture, path string) {
	t.Helper()
	bearerCall(t, f, g.automation, "GET", "/api/v1/automation/context", nil, 200, nil)
	f.browser(t).call("POST", "/api/v1/auth/invitations/validate", map[string]string{"token": g.invitation}, 204, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", g.enrollment, 200, nil)
}

func secondOwner(t *testing.T, f *fixture, ws uuid.UUID, email string) (*browser, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	owner, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Name: "Second owner", Email: email, PasswordHash: owner.PasswordHash})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.Queries.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: second.ID, Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	b.login(email, f.password)
	return b, second.ID
}

func auditedBy(t *testing.T, f *fixture, ws, actor uuid.UUID, action string) int {
	t.Helper()
	var n int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND actor_id=$2 AND action=$3", ws, actor, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOwnerGrantsEndWithTheirOwnership(t *testing.T) {
	f := setup(t)
	owner, scope, _, path := automationEnvironment(t, f)
	ws := scope.WorkspaceId
	members := "/api/v1/workspaces/" + ws.String() + "/members/"

	for _, change := range []struct {
		name   string
		end    func(uuid.UUID)
		revive bool
	}{
		{"demoted", func(id uuid.UUID) {
			owner.call("PUT", members+id.String(), map[string]string{"role": "operator"}, 204, nil)
		}, true},
		{"removed", func(id uuid.UUID) { owner.call("DELETE", members+id.String(), nil, 204, nil) }, false},
	} {
		t.Run(change.name, func(t *testing.T) {
			b, id := secondOwner(t, f, ws, change.name+"-owner@example.test")
			kept := issueOwnerGrants(t, f, owner, ws, path, change.name+"-kept@example.test")
			ended := issueOwnerGrants(t, f, b, ws, path, change.name+"-ended@example.test")
			actions := []string{"automation.revoke", "invitation.revoke", "server.enrollment_withdraw"}
			before := map[string]int{}
			for _, action := range actions {
				before[action] = auditedBy(t, f, ws, f.owner, action)
			}
			change.end(id)
			ended.refused(t, f, path)
			for _, action := range actions {
				if auditedBy(t, f, ws, f.owner, action) <= before[action] {
					t.Fatalf("%s was not recorded against the owner who ended the authority", action)
				}
			}
			var invitations []api.Invitation
			owner.call("GET", "/api/v1/workspaces/"+ws.String()+"/invitations", nil, 200, &invitations)
			for _, v := range invitations {
				if string(v.Email) == change.name+"-ended@example.test" {
					t.Fatal("a withdrawn invitation is still listed")
				}
			}
			if change.revive {
				// Ownership given back is new authority; it does not bring back what the old one issued.
				owner.call("PUT", members+id.String(), map[string]string{"role": "owner"}, 204, nil)
				ended.refused(t, f, path)
			}
			// Another owner's grants are untouched.
			kept.work(t, f, path)
		})
	}
}

func TestOperatorRoleChangesLeaveOwnerGrants(t *testing.T) {
	f := setup(t)
	owner, scope, _, path := automationEnvironment(t, f)
	ws := scope.WorkspaceId
	b, id := secondOwner(t, f, ws, "still-owner@example.test")
	grants := issueOwnerGrants(t, f, b, ws, path, "still-invited@example.test")
	members := "/api/v1/workspaces/" + ws.String() + "/members/"
	// Saving an owner as an owner is not a demotion.
	owner.call("PUT", members+id.String(), map[string]string{"role": "owner"}, 204, nil)
	grants.work(t, f, path)
}

func TestIssuerCredentialChangeVoidsInvitationAndEnrollmentCode(t *testing.T) {
	f := setup(t)
	owner, scope, _, path := automationEnvironment(t, f)
	ws := scope.WorkspaceId
	grants := issueOwnerGrants(t, f, owner, ws, path, "before-change@example.test")
	var renewed api.Session
	owner.call("POST", "/api/v1/auth/password", map[string]string{"password": "replacement password here"}, 200, &renewed)
	owner.csrf = renewed.CsrfToken
	grants.refused(t, f, path)
	var invitations []api.Invitation
	owner.call("GET", "/api/v1/workspaces/"+ws.String()+"/invitations", nil, 200, &invitations)
	if len(invitations) != 0 {
		t.Fatal("an invitation issued before its issuer's credential change is still listed")
	}
	// What the owner issues under their new credentials works.
	again := issueOwnerGrants(t, f, owner, ws, path, "after-change@example.test")
	again.work(t, f, path)
}
