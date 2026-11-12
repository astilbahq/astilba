package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// lapses are the hosted entitlement states in which management is inactive. Revoking access must
// work in every one of them, including long after recovery has closed.
var lapses = []struct {
	name string
	// ended is how long ago management ended; nil means there never was an entitlement.
	ended *time.Duration
}{
	{"within the recovery window", new(time.Minute)},
	{"past the recovery window", new(hosting.GracePeriod + time.Hour)},
	{"never subscribed", nil},
}

// lapse makes the fixture a hosted installation whose workspace ws has the given entitlement.
func lapse(t *testing.T, f *fixture, ws uuid.UUID, ended *time.Duration) {
	t.Helper()
	ctx := context.Background()
	f.st.Hosted = true
	if _, err := f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=now() WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	var until *time.Time
	if ended != nil {
		at := time.Now().Add(-*ended)
		until = &at
	}
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',$2,4)", ws, until); err != nil {
		t.Fatal(err)
	}
}

func refusedForSubscription(t *testing.T, what string, raw []byte) {
	t.Helper()
	var e api.Error
	if err := json.Unmarshal(raw, &e); err != nil || e.Code != "subscription_required" {
		t.Fatalf("%s was not refused for the subscription: %s", what, raw)
	}
}

// An owner of a lapsed hosted workspace can still take every kind of access away, while every
// action that grants access is still refused.
func TestLapsedWorkspaceOwnerCanRevokeAccess(t *testing.T) {
	for _, state := range lapses {
		t.Run(state.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := f.browser(t)
			workspace := b.login("owner@example.test", f.password).Workspaces[0]
			ws := workspace.Id
			enableMFA(t, b)
			prefix := "/api/v1/workspaces/" + ws.String()
			var project api.Project
			b.call("POST", prefix+"/projects", map[string]string{"name": "Lapse"}, 201, &project)
			var env api.Environment
			b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
			envPath := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()

			// Members and invitations.
			member := func(email, role string) db.Account {
				a, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: email, Name: role, PasswordHash: f.auth.Passwords.Dummy})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.st.Queries.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: a.ID, Role: role}); err != nil {
					t.Fatal(err)
				}
				return a
			}
			departed, demoted, viewer := member("departed@example.test", "owner"), member("demoted@example.test", "owner"), member("viewer@example.test", "viewer")
			owner, err := f.st.Queries.AccountByID(ctx, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			invite := func(email string) (uuid.UUID, string) {
				token, err := cryptobox.Token()
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.New()
				if err = f.st.Queries.InsertInvitation(ctx, db.InsertInvitationParams{WorkspaceID: ws, ID: id, Email: email, Role: "operator", TokenHash: auth.Hash(token), ExpiresAt: time.Now().Add(time.Hour), IssuerID: owner.ID, IssuerAuthVersion: owner.AuthVersion}); err != nil {
					t.Fatal(err)
				}
				return id, token
			}
			revokedInvitation, revokedToken := invite("revoked@example.test")
			_, pendingToken := invite("pending@example.test")

			// An automation credential, servers, a terminal policy and a Git source.
			credential := issueAutomation(t, b, envPath, domain.EnvironmentRead)
			bearerCall(t, f, *credential.Token, "GET", envPath+"/draft", nil, 200, nil)
			revoked, _, _ := enrollment(t, f, b, ws, "deployment")
			unenrolled, unenrolledIn, _ := enrollment(t, f, b, ws, "deployment")
			keyed, keyedIn, _ := enrollment(t, f, b, ws, "deployment")
			tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
			t.Cleanup(tlsServer.Close)
			if err := newKeyAgent(t, tlsServer, keyedIn, agent.InsecureKeyStore()).c.Enroll(ctx, keyed.Token); err != nil {
				t.Fatal(err)
			}
			b.call("PUT", envPath+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
			connection := uuid.New()
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,'{"authentication":"github_app"}','')`, connection, ws); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO github_app_connections(workspace_id,connection_id,app_id,installation_id,repository_id,github_user_id,actor_id) VALUES($1,$2,1,2,3,4,$3)`, ws, connection, f.owner); err != nil {
				t.Fatal(err)
			}
			// Approved by another owner, so turning auto-deploy off can show it hands nobody the approval.
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO git_sources(workspace_id,environment_id,connection_id,configuration,status,policy_actor) VALUES($1,$2,$3,'{"autoDeploy":true}','ready',$4)`, ws, env.Id, connection, demoted.ID); err != nil {
				t.Fatal(err)
			}

			lapse(t, f, ws, state.ended)

			// Granting stays refused.
			refusedForSubscription(t, "promotion", b.call("PUT", prefix+"/members/"+viewer.ID.String(), map[string]string{"role": "operator"}, 400, nil))
			refusedForSubscription(t, "an invitation", b.call("POST", prefix+"/invitations", map[string]string{"email": "new@example.test", "role": "viewer"}, 400, nil))
			refusedForSubscription(t, "accepting an invitation", f.browser(t).call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": pendingToken, "name": "Pending", "password": "a long enough password for the fixture"}, 400, nil))
			if !rowExists(t, f, "SELECT 1 FROM invitations WHERE workspace_id=$1 AND email='pending@example.test'", ws) {
				t.Fatal("a refused acceptance used up the invitation")
			}
			refusedForSubscription(t, "a new credential", b.call("POST", envPath+"/automation-credentials", map[string]any{"name": "CI", "expiresAt": time.Now().Add(time.Hour), "capabilities": []string{domain.EnvironmentRead}}, 400, nil))
			refusedForSubscription(t, "renaming the workspace", b.call("PUT", prefix, map[string]any{"name": "Renamed", "requireMfa": false}, 400, nil))
			var refusal api.Error
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", unenrolledIn, 400, &refusal)
			if refusal.Code != "subscription_required" {
				t.Fatal("enrolling a server was not refused for the subscription", refusal)
			}
			if rowExists(t, f, "SELECT 1 FROM servers WHERE id=$1 AND credential_hash IS NOT NULL", unenrolled.Server.Id) {
				t.Fatal("a lapsed workspace enrolled a server")
			}

			// Members.
			b.call("PUT", prefix+"/members/"+demoted.ID.String(), map[string]string{"role": "viewer"}, 204, nil)
			b.call("DELETE", prefix+"/members/"+departed.ID.String(), nil, 204, nil)
			if err := f.st.InWorkspace(ctx, browserPrincipal(t, f.st, departed, true), ws, store.WorkspaceAccess{}, func(*db.Queries, store.Workspace) error { return nil }); !errors.Is(err, domain.NotFound) {
				t.Fatal("a removed member kept access", err)
			}
			var role string
			if err := f.st.Pool.QueryRow(ctx, "SELECT role FROM memberships WHERE workspace_id=$1 AND account_id=$2", ws, demoted.ID).Scan(&role); err != nil || role != "viewer" {
				t.Fatal("demotion did not take effect", role, err)
			}

			// Invitations: a revoked one can't be accepted.
			b.call("DELETE", prefix+"/invitations/"+revokedInvitation.String(), nil, 204, nil)
			if rowExists(t, f, "SELECT 1 FROM invitations WHERE id=$1", revokedInvitation) {
				t.Fatal("the invitation was not revoked")
			}
			f.browser(t).call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": revokedToken, "name": "Revoked", "password": "a long enough password for the fixture"}, 400, nil)
			if rowExists(t, f, "SELECT 1 FROM memberships m JOIN accounts a ON a.id=m.account_id WHERE a.email='revoked@example.test'") {
				t.Fatal("a revoked invitation was accepted")
			}

			// Automation credentials.
			b.call("DELETE", envPath+"/automation-credentials/"+credential.Credential.Id.String(), nil, 204, nil)
			bearerCall(t, f, *credential.Token, "GET", envPath+"/draft", nil, 401, nil)

			// Servers.
			b.call("POST", prefix+"/servers/"+keyed.Server.Id.String()+"/rotate", nil, 204, nil)
			b.call("POST", prefix+"/servers/"+keyed.Server.Id.String()+"/network-removal", nil, 204, nil)
			b.call("POST", prefix+"/servers/"+revoked.Server.Id.String()+"/revoke", nil, 204, nil)
			if !rowExists(t, f, "SELECT 1 FROM servers WHERE id=$1 AND revoked", revoked.Server.Id) || !rowExists(t, f, "SELECT 1 FROM servers WHERE id=$1 AND rotate_key_requested", keyed.Server.Id) {
				t.Fatal("server revocation or rotation did not take effect")
			}

			// Terminal access: narrowing, then turning off, then ending what is open.
			b.call("PUT", envPath+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true, OwnersOnly: true, Generation: 1}, 200, nil)
			refusedForSubscription(t, "widening terminal access", b.call("PUT", envPath+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true, Generation: 2}, 400, nil))
			b.call("PUT", envPath+"/troubleshooting", api.SaveTroubleshootingPolicy{Generation: 2}, 200, nil)
			b.call("POST", prefix+"/troubleshooting-sessions/close", nil, 204, nil)

			// Git automation.
			b.call("PUT", envPath+"/source/policy", map[string]bool{"autoDeploy": false}, 204, nil)
			refusedForSubscription(t, "turning automatic deployment on", b.call("PUT", envPath+"/source/policy", map[string]bool{"autoDeploy": true}, 400, nil))
			if rowExists(t, f, "SELECT 1 FROM git_sources WHERE environment_id=$1 AND ((configuration->>'autoDeploy')::boolean OR policy_actor<>$2)", env.Id, demoted.ID) {
				t.Fatal("automatic deployment stayed on, or turning it off changed who approved the source")
			}
			// Disconnecting also pauses the source, so it comes after turning auto-deploy off.
			b.call("POST", prefix+"/git-connections/"+connection.String()+"/disconnect", nil, 204, nil)
			if rowExists(t, f, "SELECT 1 FROM github_app_connections WHERE connection_id=$1 AND active", connection) {
				t.Fatal("the GitHub connection stayed active")
			}

			// Requiring two-factor authentication.
			b.call("PUT", prefix, map[string]any{"name": workspace.Name, "requireMfa": true}, 200, nil)
			refusedForSubscription(t, "dropping the MFA requirement", b.call("PUT", prefix, map[string]any{"name": workspace.Name, "requireMfa": false}, 400, nil))
		})
	}
}

// Pausing a preview policy stays possible on a lapsed workspace; resuming it does not.
func TestLapsedWorkspaceOwnerCanPausePreviews(t *testing.T) {
	h := newHungPreview(t)
	var policy api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &policy)
	enableMFA(t, h.b)
	lapse(t, h.f, h.ws, new(hosting.GracePeriod+time.Hour))
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": false, "generation": policy.Generation, "configuration": h.policy}, 200, nil)
	if rowExists(t, h.f, "SELECT 1 FROM preview_policies WHERE workspace_id=$1 AND enabled", h.ws) {
		t.Fatal("the preview policy stayed enabled")
	}
	refusedForSubscription(t, "resuming previews", h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": h.policy}, 400, nil))
}
