package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
)

func TestInvitationValidationDoesNotAcceptOrChangeAuthentication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	guest := f.browser(t)
	token := seedInvitation(t, f, "invited-new@example.test")
	invitation, err := f.st.Queries.PeekInvitation(ctx, auth.Hash(token))
	if err != nil {
		t.Fatal(err)
	}
	var accounts, memberships int
	if err = f.st.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM accounts),(SELECT count(*) FROM memberships)").Scan(&accounts, &memberships); err != nil {
		t.Fatal(err)
	}
	check := func(token string, status int) {
		t.Helper()
		body := guest.call("POST", "/api/v1/auth/invitations/validate", map[string]string{"token": token}, status, nil)
		if status == 204 {
			if len(body) != 0 {
				t.Fatal("invitation validity check returned metadata")
			}
		} else {
			var failure api.Error
			if err := json.Unmarshal(body, &failure); err != nil || failure.Code != "invalid_credentials" {
				t.Fatal("invitation validity check did not return a generic credential error", err)
			}
			for _, private := range []string{token, invitation.Email, invitation.WorkspaceID.String(), invitation.ID.String()} {
				if strings.Contains(string(body), private) {
					t.Fatal("invitation validity check leaked capability or workspace information")
				}
			}
		}
	}
	check("unknown-invitation-token", 400)
	check(token, 204)
	check(token, 204)
	var afterAccounts, afterMemberships int
	if err = f.st.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM accounts),(SELECT count(*) FROM memberships)").Scan(&afterAccounts, &afterMemberships); err != nil {
		t.Fatal(err)
	}
	if afterAccounts != accounts || afterMemberships != memberships || guest.session().User != nil {
		t.Fatal("invitation validity check created an account, membership or authenticated session")
	}
	stillValid, err := f.st.Queries.PeekInvitation(ctx, auth.Hash(token))
	if err != nil || !stillValid.ExpiresAt.Equal(invitation.ExpiresAt) || stillValid.ID != invitation.ID {
		t.Fatal("invitation validity check consumed or extended the invitation", err)
	}
	if _, err = f.st.Pool.Exec(ctx, "UPDATE invitations SET expires_at=now()-interval '1 second' WHERE token_hash=$1", auth.Hash(token)); err != nil {
		t.Fatal(err)
	}
	check(token, 400)
	if _, err = f.st.Pool.Exec(ctx, "UPDATE invitations SET expires_at=now()+interval '1 hour' WHERE token_hash=$1", auth.Hash(token)); err != nil {
		t.Fatal(err)
	}
	check(token, 204)
	var accepted api.Session
	guest.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": token, "name": "Invited viewer", "password": f.password}, 200, &accepted)
	guest.csrf = accepted.CsrfToken
	check(token, 400)
	if accepted.User == nil || string(accepted.User.Email) != invitation.Email || len(accepted.Workspaces) != 1 || accepted.Workspaces[0].Id != invitation.WorkspaceID {
		t.Fatal("invitation could not be accepted after validity checks")
	}
	revoked := seedInvitation(t, f, "revoked@example.test")
	if _, err = f.st.Pool.Exec(ctx, "DELETE FROM invitations WHERE token_hash=$1", auth.Hash(revoked)); err != nil {
		t.Fatal(err)
	}
	check(revoked, 400)
	replacement := seedInvitation(t, f, "replaced@example.test")
	if _, err = f.st.Pool.Exec(ctx, "UPDATE invitations SET token_hash=$1 WHERE token_hash=$2", auth.Hash("replacement-invitation-token"), auth.Hash(replacement)); err != nil {
		t.Fatal(err)
	}
	check(replacement, 400)
}
