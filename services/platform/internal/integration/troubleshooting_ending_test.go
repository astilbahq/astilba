package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/google/uuid"
)

// The sweep closes an offer nobody attached to and leaves a live relay alone, whatever
// its ticket says. A relay waiting for its other side records why it ends, however its
// clock compares with the database's, unless a minute shows it could not.
func TestTroubleshootingSweepClosesOnlyAbandonedOffers(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	live, _, _ := x.connected(t, x.owner, 1)
	abandoned := x.open(t, x.owner, x.app2, 1, 201)
	operator := x.member(t, "operator@example.test", "operator")
	waiting := x.open(t, operator, x.app, 2, 201)
	stranded := x.open(t, operator, x.app, 3, 201)
	for _, session := range []uuid.UUID{waiting.Session.Id, stranded.Session.Id} {
		if agent, status := x.dialAgent(t, session, x.agentToken); agent == nil {
			t.Fatalf("agent stream refused: %d", status)
		}
	}
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()-interval '1 second' WHERE workspace_id=$1", x.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()-interval '2 minutes' WHERE id=$1", stranded.Session.Id); err != nil {
		t.Fatal(err)
	}
	var state string
	for deadline := time.Now().Add(45 * time.Second); state != "closed" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", abandoned.Session.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
	}
	x.waitClosed(t, abandoned.Session.Id, "expired")
	x.waitClosed(t, stranded.Session.Id, "expired")
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", live.Session.Id).Scan(&state); err != nil || state != "active" {
		t.Fatal("the sweep closed a live relay", state, err)
	}
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", waiting.Session.Id).Scan(&state); err != nil || state != "offered" {
		t.Fatal("the sweep closed a relay waiting for its other side", state, err)
	}
}

// A workspace whose lock is held waits for a later sweep. Waiting for it would keep every
// other due workspace locked until its holder finished.
func TestTroubleshootingSweepSkipsAHeldWorkspace(t *testing.T) {
	x := relayFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	x.open(t, x.owner, x.app, 1, 201)
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()-interval '1 second' WHERE workspace_id=$1", x.ws); err != nil {
		t.Fatal(err)
	}
	holder, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err = db.New(holder).LockWorkspaceByID(ctx, x.ws); err != nil {
		t.Fatal(err)
	}
	due, err := x.f.st.Queries.LockTroubleshootingSweepWorkspaces(ctx)
	if err != nil {
		t.Fatal("the sweep waited for a held workspace: ", err)
	}
	if slices.Contains(due, x.ws) {
		t.Fatal("the sweep locked a held workspace")
	}
}

// Recording how a stream ended has a short budget, so it does not wait behind a long
// workspace lock such as an export, and cannot deadlock with an owner closing the session.
func TestTroubleshootingEndDoesNotWaitForTheWorkspace(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened, agent, browser := x.connected(t, x.owner, 1)
	holder, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err = db.New(holder).LockWorkspaceByID(ctx, x.ws); err != nil {
		t.Fatal(err)
	}
	agent.send(t, []byte{terminal.Exit})
	browser.drained(t)
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
