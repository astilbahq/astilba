package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

// TestNewWorkspacesRequireMFA covers the two-factor requirement a workspace starts with: on for
// the bootstrapped workspace and one created later, off and unchanged for one made before, and
// never a lockout, because the owner can set up two-factor authentication and then change it.
func TestNewWorkspacesRequireMFA(t *testing.T) {
	f := setupBootstrap(t, false)
	ctx := context.Background()
	hash, err := fixturePasswordHash()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.st.Bootstrap(ctx, "owner@example.test", "Owner", hash, "Bootstrapped")
	if err != nil {
		t.Fatal(err)
	}
	// A workspace made before new ones required two-factor authentication: the column's
	// default is the setting each of them has.
	earlier := uuid.New()
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Earlier')`, earlier); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, earlier, owner); err != nil {
		t.Fatal(err)
	}

	b := f.browser(t)
	session := b.login("owner@example.test", f.password)
	required := map[uuid.UUID]bool{}
	for _, w := range session.Workspaces {
		required[w.Id] = w.RequireMfa
	}
	var bootstrapped uuid.UUID
	for id := range required {
		if id != earlier {
			bootstrapped = id
		}
	}
	if len(required) != 2 || !required[bootstrapped] || required[earlier] {
		t.Fatalf("bootstrapped workspace must require two-factor authentication and the earlier one must not: %+v", session.Workspaces)
	}

	var created api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Created"}, 201, &created)
	if !created.RequireMfa {
		t.Fatal("a created workspace must start requiring two-factor authentication")
	}
	var read api.Workspace
	b.call("GET", "/api/v1/workspaces/"+created.Id.String(), nil, 200, &read)
	if !read.RequireMfa {
		t.Fatal("the created workspace's stored requirement differs from its response")
	}

	// Before enrolling, the owner reads both new workspaces but changes neither; the earlier
	// workspace keeps the rule it had.
	for _, id := range []uuid.UUID{bootstrapped, created.Id} {
		b.call("GET", "/api/v1/workspaces/"+id.String()+"/projects", nil, 200, nil)
		var failure api.Error
		b.call("POST", "/api/v1/workspaces/"+id.String()+"/projects", map[string]string{"name": "Before"}, 403, &failure)
		if failure.Code != "mfa_required" {
			t.Fatalf("an unenrolled owner's change was refused with %q, not mfa_required", failure.Code)
		}
	}
	b.call("POST", "/api/v1/workspaces/"+earlier.String()+"/projects", map[string]string{"name": "Earlier"}, 201, nil)
	if !workspaceRequires(t, b, earlier, false) {
		t.Fatal("creating workspaces changed the earlier workspace's requirement")
	}

	// Setting up two-factor authentication needs no workspace access, and the session it renews
	// changes the new workspaces.
	var enrollment api.MFAEnrollment
	b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codes api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": code, "current": map[string]string{"password": f.password}}, 200, &codes)
	b.csrf = codes.Session.CsrfToken
	for _, id := range []uuid.UUID{bootstrapped, created.Id} {
		b.call("POST", "/api/v1/workspaces/"+id.String()+"/projects", map[string]string{"name": "After"}, 201, nil)
	}
}

// workspaceRequires reports whether the browser's session lists the workspace id with require.
func workspaceRequires(t *testing.T, b *browser, id uuid.UUID, require bool) bool {
	t.Helper()
	for _, w := range b.session().Workspaces {
		if w.Id == id {
			return w.RequireMfa == require
		}
	}
	t.Fatalf("session does not list workspace %s", id)
	return false
}
