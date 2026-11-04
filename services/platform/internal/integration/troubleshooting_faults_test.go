package integration

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/google/uuid"
)

// A brief fault does not end a session, but a person whose conditions can no longer be
// read does not keep a shell.
func TestTroubleshootingEndsWhenItsChecksStall(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	x.f.apiServer.TerminalReviewEvery = 2 * time.Second
	opened, _, _ := x.connected(t, x.owner, 1)
	stall, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stall.Rollback(ctx)
	if _, err = stall.Exec(ctx, "LOCK TABLE hosted_subscriptions IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second) // past the first review's time limit, short of the second's
	var state string
	if err = x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&state); err != nil || state != "active" {
		t.Fatal("one failed review ended the session", state, err)
	}
	x.waitClosed(t, opened.Session.Id, "session checks failed")
}

// With no relay left to close it, a row whose end could not be written is retried, so it
// does not hold its limits until the deadline or lose its byte counts.
func TestTroubleshootingEndSurvivesADatabaseFault(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened, agent, browser := x.connected(t, x.owner, 1)
	browser.send(t, append([]byte{terminal.Input}, "typed"...))
	agent.expect(t, append([]byte{terminal.Input}, "typed"...))
	fault, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Rollback(ctx)
	if _, err = fault.Exec(ctx, "LOCK TABLE audit_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	agent.send(t, []byte{terminal.Exit})
	browser.expect(t, []byte{terminal.Exit})
	browser.drained(t)
	time.Sleep(6 * time.Second) // past the first attempt's limit
	var state string
	if err = x.f.st.Pool.QueryRow(ctx, "SELECT state FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&state); err != nil || state != "active" {
		t.Fatal("the end was recorded despite the fault", state, err)
	}
	if err = fault.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	x.waitClosed(t, opened.Session.Id, "shell exited")
	var input int64
	if err = x.f.st.Pool.QueryRow(ctx, "SELECT input_bytes FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&input); err != nil || input != 5 {
		t.Fatal("the byte counts were lost", input, err)
	}
	x.audited(t, "session.close", 1)
}

// waitAttached waits for the browser's ticket to be redeemed.
func (x troubleshootingFixture) waitAttached(t *testing.T, session uuid.UUID) {
	t.Helper()
	var attached bool
	for deadline := time.Now().Add(10 * time.Second); !attached && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT browser_attached_at IS NOT NULL FROM troubleshooting_sessions WHERE id=$1", session).Scan(&attached); err != nil {
			t.Fatal(err)
		}
	}
	if !attached {
		t.Fatal("the ticket was not redeemed")
	}
}

// Someone who lost access while their browser waited for the agent gets no shell.
func TestTroubleshootingStartRereadsConditions(t *testing.T) {
	x := relayFixture(t)
	operator := x.member(t, "operator@example.test", "operator")
	opened := x.open(t, operator, x.app, 1, 201)
	x.attachBrowser(t, operator, opened)
	x.waitAttached(t, opened.Session.Id)
	operator.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	x.waitClosed(t, opened.Session.Id, "browser session ended")
	for frame := range agent.frames {
		if len(frame) > 0 && frame[0] == terminal.Start {
			t.Fatal("the shell was told to start")
		}
	}
}

// A browser that leaves while waiting for the agent ends the session there and then, even
// after sending something, and what it sends during the wait still reaches the shell.
func TestTroubleshootingNoticesABrowserLeavingTheWait(t *testing.T) {
	x := relayFixture(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	browser := x.attachBrowser(t, x.owner, opened)
	x.waitAttached(t, opened.Session.Id)
	browser.send(t, terminal.ResizeFrame(80, 24))
	browser.conn.CloseNow()
	x.waitClosed(t, opened.Session.Id, "browser stream lost")
	if agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken); agent != nil || status != 404 {
		t.Fatalf("agent attached to an ended session: %d", status)
	}

	opened = x.open(t, x.owner, x.app, 1, 201)
	browser = x.attachBrowser(t, x.owner, opened)
	x.waitAttached(t, opened.Session.Id)
	browser.send(t, append([]byte{terminal.Input}, "early"...))
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	agent.expect(t, []byte{terminal.Start})
	agent.expect(t, append([]byte{terminal.Input}, "early"...))
}

// A stalled activation ends the session rather than leaving both streams unread.
func TestTroubleshootingStalledActivationEndsTheSession(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	if _, err := x.f.st.Pool.Exec(ctx, `CREATE FUNCTION stall_activation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(30); RETURN NEW; END $$;
		CREATE TRIGGER stall_activation BEFORE UPDATE ON troubleshooting_sessions FOR EACH ROW WHEN (NEW.state = 'active') EXECUTE FUNCTION stall_activation()`); err != nil {
		t.Fatal(err)
	}
	opened := x.open(t, x.owner, x.app, 1, 201)
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	x.attachBrowser(t, x.owner, opened)
	x.waitClosed(t, opened.Session.Id, "activation failed")
	for frame := range agent.frames {
		if len(frame) > 0 && frame[0] == terminal.Start {
			t.Fatal("the shell was told to start")
		}
	}
}

// A stalled browser refresh holds up neither typing nor the idle limit.
func TestTroubleshootingStalledRefreshLeavesTheRelayRunning(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	x.f.apiServer.TerminalIdle = 8 * time.Second
	opened, agent, browser := x.connected(t, x.owner, 1)
	stall, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stall.Rollback(ctx)
	if _, err = stall.Exec(ctx, "SELECT b.token FROM sessions b JOIN troubleshooting_sessions t ON b.token=translate(rtrim(encode(t.browser_session_hash,'base64'),'='),'+/','-_') WHERE t.id=$1 FOR UPDATE OF b", opened.Session.Id); err != nil {
		t.Fatal(err)
	}
	browser.send(t, append([]byte{terminal.Input}, "x"...))
	agent.expect(t, append([]byte{terminal.Input}, "x"...))
	time.Sleep(1500 * time.Millisecond) // the refresh is now waiting for the row
	browser.send(t, append([]byte{terminal.Input}, "y"...))
	agent.expect(t, append([]byte{terminal.Input}, "y"...))
	x.waitClosed(t, opened.Session.Id, "idle timeout")
}
