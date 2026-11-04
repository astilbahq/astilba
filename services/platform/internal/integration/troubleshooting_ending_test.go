package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/google/uuid"
)

// The sweep closes an offer nobody attached to and leaves a live relay alone, whatever
// its ticket says.
func TestTroubleshootingSweepClosesOnlyAbandonedOffers(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	live, _, _ := x.connected(t, x.owner, 1)
	abandoned := x.open(t, x.owner, x.app2, 1, 201)
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()-interval '1 second' WHERE workspace_id=$1", x.ws); err != nil {
		t.Fatal(err)
	}
	var state string
	for deadline := time.Now().Add(45 * time.Second); state != "closed" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", abandoned.Session.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
	}
	x.waitClosed(t, abandoned.Session.Id, "expired")
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", live.Session.Id).Scan(&state); err != nil || state != "active" {
		t.Fatal("the sweep closed a live relay", state, err)
	}
}

// An owner acting on a session while it ends on its own does not deadlock with it.
func TestTroubleshootingEndTakesTheWorkspaceFirst(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened, agent, browser := x.connected(t, x.owner, 1)
	owner, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Rollback(ctx)
	if _, err = owner.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", x.ws); err != nil {
		t.Fatal(err)
	}
	agent.send(t, []byte{terminal.Exit})
	browser.drained(t)
	time.Sleep(time.Second) // the end is now waiting for the workspace
	if _, err = owner.Exec(ctx, "SELECT id FROM troubleshooting_sessions WHERE id=$1 FOR UPDATE", opened.Session.Id); err != nil {
		t.Fatal("the session row was held while waiting for the workspace", err)
	}
	if err = owner.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	x.waitClosed(t, opened.Session.Id, "shell exited")
}

// Stopping access is not harder than granting it: the write requirements that block
// opening a session do not block ending one, and the role rules still apply.
func TestTroubleshootingEndsWithoutTheWriteRequirements(t *testing.T) {
	x := deployedTroubleshootingEnvironment(t)
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
	x.optIn(t)
	viewer := x.member(t, "viewer@example.test", "viewer")
	operator := x.member(t, "operator@example.test", "operator")
	operators := x.open(t, operator, x.app, 1, 201)
	owners := x.open(t, x.owner, x.app2, 1, 201)
	x.f.st.Hosted = true
	x.owner.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: 2}, 403, nil)

	close := func(b *browser, session uuid.UUID, status int) {
		t.Helper()
		b.call("POST", x.prefix+"/troubleshooting-sessions/"+session.String()+"/close", nil, status, nil)
	}
	close(operator, owners.Session.Id, 403)
	close(viewer, owners.Session.Id, 403)
	operator.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 403, nil)
	close(operator, operators.Session.Id, 204)
	x.waitClosed(t, operators.Session.Id, "closed")
	x.owner.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 204, nil)
	x.waitClosed(t, owners.Session.Id, "closed by owner")
	x.audited(t, "session.close", 2)
}

// A refusal only the agent can make is audited like one at open, once per person and
// reason each hour.
func TestTroubleshootingAuditsAgentRefusals(t *testing.T) {
	x := relayFixture(t)
	for instance := 1; instance <= 2; instance++ {
		opened := x.open(t, x.owner, x.app, instance, 201)
		agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
		if agent == nil {
			t.Fatalf("agent stream refused: %d", status)
		}
		browser := x.attachBrowser(t, x.owner, opened)
		agent.expect(t, []byte{terminal.Start})
		agent.send(t, append([]byte{terminal.Refused}, terminal.RefusedNoShell...))
		browser.expect(t, append([]byte{terminal.Refused}, terminal.RefusedNoShell...))
		x.waitClosed(t, opened.Session.Id, terminal.RefusedEnding+terminal.RefusedNoShell)
	}
	x.audited(t, "session.refused.no_shell", 1)
	x.audited(t, "session.close", 2)
}
