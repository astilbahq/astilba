package integration

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// An owner can retire one superseded version of a secret, to clear a bad value or one the
// runtime-secret check no longer needs to hold, without giving up the secret. The current version
// is replaced by a new one instead, and one that may still deploy stays.
func TestRetireSecretVersion(t *testing.T) {
	x := newRetirementFixture(t)
	_, path, draft, deployed := x.secretEnvironment("Production", "versions.example.test")
	x.deploy(path, draft)
	var unused, current api.SecretVersion
	x.owner.call("POST", path+"/secrets/"+deployed.SecretId.String()+"/versions", map[string]string{"value": "dummy-unused-value"}, 201, &unused)
	x.owner.call("POST", path+"/secrets/"+deployed.SecretId.String()+"/versions", map[string]string{"value": "dummy-current-value"}, 201, &current)
	version := func(v api.SecretVersion) string {
		return path + "/secrets/" + v.SecretId.String() + "/versions/" + v.Id.String()
	}

	// Who may ask.
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	operator := x.f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": "Operator", "password": x.f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	operator.call("DELETE", version(unused), nil, 403, nil)
	x.f.auth.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	x.owner.call("DELETE", version(unused), nil, 403, nil)
	x.f.auth.Now = time.Now

	// The current version is replaced, never retired on its own.
	x.refusal(x.owner, "DELETE", version(current), nil, "secretVersion.current")
	// The draft and the running deployment name the deployed version, and then the deployment alone.
	x.refusal(x.owner, "DELETE", version(deployed), nil, "secretVersion.inUse")
	_, spec := draftSpec(t, x.owner, path)
	spec.Applications[0].Variables = []domain.Variable{}
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	x.refusal(x.owner, "DELETE", version(deployed), nil, "secretVersion.inUse")

	// A superseded version nothing names is erased, and leaves the list.
	x.owner.call("DELETE", version(unused), nil, 204, nil)
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL", x.ws, unused.Id); n != 1 {
		t.Fatal("the retired version kept its value")
	}
	if n := x.count("SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND secret_id=$2 AND erased_at IS NULL", x.ws, deployed.SecretId); n != 2 {
		t.Fatalf("versions with values: %d, want the deployed and current ones", n)
	}
	var listed []api.SecretVersion
	x.owner.call("GET", path+"/secrets", nil, 200, &listed)
	if len(listed) != 2 || listed[0].Id != current.Id || listed[1].Id != deployed.Id {
		t.Fatalf("listed after retiring a version: %+v", listed)
	}
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='secret.version.retire' AND resource_id=$2", x.ws, unused.Id); n != 1 {
		t.Fatalf("version retirement audit events: %d", n)
	}
	x.owner.call("DELETE", version(unused), nil, 404, nil)
	// A draft cannot name it again.
	spec.Applications[0].Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &unused.Id}}
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 400, nil)

	// A value too short for the leak check is flagged to members who can write secrets, and to them
	// alone: to a reader it would say the value can be guessed.
	var short api.SecretVersion
	x.owner.call("POST", path+"/secrets", map[string]string{"name": "Pin", "value": "1234"}, 201, &short)
	flagged := func(b *browser) map[string]bool {
		var listed []api.SecretVersion
		b.call("GET", path+"/secrets", nil, 200, &listed)
		out := map[string]bool{}
		for _, v := range listed {
			out[v.Name] = v.TooShortToCheck != nil && *v.TooShortToCheck
			if v.TooShortToCheck != nil && !*v.TooShortToCheck {
				t.Fatal("tooShortToCheck is sent as false")
			}
		}
		return out
	}
	for _, b := range []*browser{x.owner, operator} {
		if got := flagged(b); !got["Pin"] || got["Token"] {
			t.Fatalf("flags for a member who can write: %v", got)
		}
	}
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/invitations", map[string]string{"email": "viewer@example.test", "role": "viewer"}, 201, nil)
	viewer := x.f.browser(t)
	var joined api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": "Viewer", "password": x.f.password}, 200, &joined)
	viewer.csrf = joined.CsrfToken
	if got := flagged(viewer); got["Pin"] {
		t.Fatal("a viewer was told a value is short")
	}

	// Another environment's secret is not found through this one.
	_, other, _, foreign := x.secretEnvironment("Staging", "versions-staging.example.test")
	x.owner.call("POST", other+"/secrets/"+foreign.SecretId.String()+"/versions", map[string]string{"value": "dummy-foreign-current"}, 201, nil)
	x.owner.call("DELETE", path+"/secrets/"+foreign.SecretId.String()+"/versions/"+foreign.Id.String(), nil, 404, nil)
}
