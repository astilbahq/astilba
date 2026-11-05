package integration

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A 401, 403 or 404 tells a browser to sign out, a runner that its token is bad and a provider
// not to redeliver a push, so a failed read must answer as a failure while a missing row
// keeps its refusal.
func TestFailedReadsAreNotRefusals(t *testing.T) {
	f := setup(t)
	f.execution.Troubleshooting = true
	b, scope, _, path := automationEnvironment(t, f)
	token := *issueAutomation(t, b, path, domain.EnvironmentRead).Token
	stream := "/api/v1/workspaces/" + scope.WorkspaceId.String() + "/troubleshooting-sessions/" + uuid.NewString() + "/stream"
	webhook := "/hooks/git/" + uuid.NewString()

	// A body the validator cannot read is the client's fault, and needs no credentials to send.
	f.browser(t).call("GET", path, strings.Repeat("x", 150001), 401, nil)
	b.call("POST", webhook, map[string]string{}, 404, nil)
	for _, c := range []struct {
		table   string
		request func()
	}{
		{"accounts", func() { b.call("GET", stream, nil, 500, nil) }},
		{"automation_credentials", func() { bearerCall(t, f, token, "GET", path, nil, 500, nil) }},
		{"environments", func() { b.call("PUT", path+"/draft", map[string]string{}, 500, nil) }},
		{"git_connections", func() { b.call("POST", webhook, map[string]string{}, 500, nil) }},
	} {
		if _, err := f.st.Pool.Exec(t.Context(), "ALTER TABLE "+c.table+" RENAME TO unavailable"); err != nil {
			t.Fatal(err)
		}
		c.request()
		if _, err := f.st.Pool.Exec(t.Context(), "ALTER TABLE unavailable RENAME TO "+c.table); err != nil {
			t.Fatal(err)
		}
	}
}
