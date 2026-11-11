package integration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type terminalPeer struct {
	conn   *websocket.Conn
	frames chan []byte
	err    error // why reading stopped, set before frames closes
}

func readTerminal(conn *websocket.Conn) *terminalPeer {
	peer := &terminalPeer{conn: conn, frames: make(chan []byte, 64)}
	go func() {
		defer close(peer.frames)
		for {
			_, frame, err := conn.Read(context.Background())
			if err != nil {
				peer.err = err
				return
			}
			peer.frames <- frame
		}
	}()
	return peer
}

func (p *terminalPeer) expect(t *testing.T, want []byte) {
	t.Helper()
	select {
	case got, open := <-p.frames:
		if !open || string(got) != string(want) {
			t.Fatalf("frame %q open=%v, want %q", got, open, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("frame %q missing", want)
	}
}

func (p *terminalPeer) send(t *testing.T, frame []byte) {
	t.Helper()
	if err := p.conn.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
}

func (p *terminalPeer) drained(t *testing.T) {
	t.Helper()
	for deadline := time.After(20 * time.Second); ; {
		select {
		case _, open := <-p.frames:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("stream did not close")
		}
	}
}

// toldWhy waits for management to close the agent's stream, and checks that it gave the
// reason the session records, which the agent writes to the host's journal.
func (p *terminalPeer) toldWhy(t *testing.T, reason string) {
	t.Helper()
	p.drained(t)
	if got, closed := terminal.Ending(p.err); !closed || got != reason {
		t.Fatalf("agent stream ended with %v, want a close saying %q", p.err, reason)
	}
}

func (x troubleshootingFixture) websocketURL(path string) string {
	return "ws" + strings.TrimPrefix(x.f.server.URL, "http") + path
}

func (x troubleshootingFixture) dialAgent(t *testing.T, session uuid.UUID, token string) (*terminalPeer, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, x.websocketURL("/agent/v1/troubleshooting/"+session.String()+"/stream"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, response.StatusCode
	}
	t.Cleanup(func() { conn.CloseNow() })
	return readTerminal(conn), response.StatusCode
}

func (x troubleshootingFixture) dialBrowser(t *testing.T, b *browser, session uuid.UUID, origin string) (*websocket.Conn, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, x.websocketURL(x.prefix+"/troubleshooting-sessions/"+session.String()+"/stream"), &websocket.DialOptions{HTTPClient: b.client, HTTPHeader: http.Header{"Origin": {origin}}})
	if err != nil {
		return nil, response.StatusCode
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, response.StatusCode
}

func (x troubleshootingFixture) attachBrowser(t *testing.T, b *browser, opened api.OpenedTroubleshootingSession) *terminalPeer {
	t.Helper()
	conn, status := x.dialBrowser(t, b, opened.Session.Id, x.f.server.URL)
	if conn == nil {
		t.Fatalf("browser stream refused: %d", status)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(opened.Ticket)); err != nil {
		t.Fatal(err)
	}
	return readTerminal(conn)
}

// connected opens a session and attaches both streams up to the agent's start signal.
func (x troubleshootingFixture) connected(t *testing.T, b *browser, instance int) (api.OpenedTroubleshootingSession, *terminalPeer, *terminalPeer) {
	t.Helper()
	opened := x.open(t, b, x.app, instance, 201)
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	browser := x.attachBrowser(t, b, opened)
	agent.expect(t, []byte{terminal.Start})
	return opened, agent, browser
}

func (x troubleshootingFixture) waitClosed(t *testing.T, session uuid.UUID, reason string) {
	t.Helper()
	if got := x.closeReason(t, session, 20*time.Second); got != reason {
		t.Fatalf("session closed %q, want %q", got, reason)
	}
}

func relayFixture(t *testing.T) troubleshootingFixture {
	x := deployedTroubleshootingEnvironment(t)
	x.f.apiServer.TerminalReviewEvery = 100 * time.Millisecond
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
	x.optIn(t)
	return x
}

// The shell's start signal waits for both people-facing checks, frames pass through in
// the right directions only, and only metadata about them is kept.
func TestTroubleshootingRelayCarriesASession(t *testing.T) {
	x := relayFixture(t)
	opened := x.open(t, x.owner, x.app, 1, 201)

	var poll api.AgentPoll
	polled := time.Now()
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/poll", x.agentToken, nil, 200, &poll)
	if time.Since(polled) > 10*time.Second {
		t.Fatal("an offer must wake the poll: the ticket lasts only 30 seconds")
	}
	if poll.TroubleshootingOffers == nil || len(*poll.TroubleshootingOffers) != 1 || (*poll.TroubleshootingOffers)[0].SessionId != opened.Session.Id || (*poll.TroubleshootingOffers)[0].ApplicationId != x.app {
		t.Fatalf("offer: %+v", poll.TroubleshootingOffers)
	}

	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	select {
	case frame := <-agent.frames:
		t.Fatalf("agent received %q before the browser attached", frame)
	case <-time.After(300 * time.Millisecond):
	}
	if second, status := x.dialAgent(t, opened.Session.Id, x.agentToken); second != nil || status != 404 {
		t.Fatalf("second agent stream admitted: %d", status)
	}
	browser := x.attachBrowser(t, x.owner, opened)
	agent.expect(t, []byte{terminal.Start})

	browser.send(t, append([]byte{terminal.Input}, "ls\n"...))
	agent.expect(t, append([]byte{terminal.Input}, "ls\n"...))
	browser.send(t, terminal.ResizeFrame(80, 24))
	agent.expect(t, terminal.ResizeFrame(80, 24))
	agent.send(t, append([]byte{terminal.Output}, "bin etc\n"...))
	browser.expect(t, append([]byte{terminal.Output}, "bin etc\n"...))
	agent.send(t, []byte{terminal.Exit, 0})
	browser.expect(t, []byte{terminal.Exit, 0})
	x.waitClosed(t, opened.Session.Id, "shell exited")

	var input, output int64
	var hash []byte
	if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT input_bytes,output_bytes,browser_session_hash FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&input, &output, &hash); err != nil {
		t.Fatal(err)
	}
	if input != 3 || output != 8 || hash != nil {
		t.Fatalf("recorded input %d output %d hash %x", input, output, hash)
	}
	x.audited(t, "session.close", 1)
}

// A session's last frame reaches the browser, followed by an orderly close, while the
// person is still typing: closing over that unread input would reset the connection,
// which can destroy the frame.
func TestTroubleshootingLastFrameReachesTheBrowser(t *testing.T) {
	for name, test := range map[string]struct {
		last   []byte
		reason string
	}{
		"exit":    {[]byte{terminal.Exit, 7}, "shell exited"},
		"refusal": {append([]byte{terminal.Refused}, terminal.RefusedNoShell...), "agent refused: no_shell"},
	} {
		t.Run(name, func(t *testing.T) {
			x := relayFixture(t)
			opened, agent, browser := x.connected(t, x.owner, 1)
			go func() {
				for range agent.frames {
				}
			}()
			typing := make(chan struct{})
			go func() {
				defer close(typing)
				for browser.conn.Write(context.Background(), websocket.MessageBinary, append([]byte{terminal.Input}, "x"...)) == nil {
					time.Sleep(time.Millisecond) // paced, so the relay is mostly waiting to read
				}
			}()
			time.Sleep(100 * time.Millisecond)
			agent.send(t, test.last)
			browser.expect(t, test.last)
			browser.drained(t)
			if status := websocket.CloseStatus(browser.err); status != websocket.StatusNormalClosure {
				t.Fatalf("stream ended with %v, want a normal close", browser.err)
			}
			<-typing
			x.waitClosed(t, opened.Session.Id, test.reason)
		})
	}
}

// A browser that stops answering does not delay the agent's end.
func TestTroubleshootingStalledBrowserCannotHoldASessionOpen(t *testing.T) {
	x := relayFixture(t)
	x.f.apiServer.TerminalIdle = time.Second
	opened := x.open(t, x.owner, x.app, 1, 201)
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	stalled, status := x.dialBrowser(t, x.owner, opened.Session.Id, x.f.server.URL)
	if stalled == nil {
		t.Fatalf("browser stream refused: %d", status)
	}
	if err := stalled.Write(context.Background(), websocket.MessageText, []byte(opened.Ticket)); err != nil {
		t.Fatal(err)
	}
	agent.expect(t, []byte{terminal.Start})
	started := time.Now()
	agent.toldWhy(t, "idle timeout")
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("agent stream ended after %s, waiting on the browser", elapsed)
	}
	x.waitClosed(t, opened.Session.Id, "idle timeout")
}

// dialRawStream upgrades a stream with no WebSocket client behind it, so nothing answers
// the server unless the test does.
func (x troubleshootingFixture) dialRawStream(t *testing.T, path, headers string) (net.Conn, *bufio.Reader) {
	t.Helper()
	address := x.f.server.Listener.Addr().String()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err = conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n%sConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", path, address, headers)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("stream refused: %v %v", response, err)
	}
	return conn, reader
}

func (x troubleshootingFixture) dialRawAgent(t *testing.T, session uuid.UUID) (net.Conn, *bufio.Reader) {
	t.Helper()
	return x.dialRawStream(t, "/agent/v1/troubleshooting/"+session.String()+"/stream", "Authorization: Bearer "+x.agentToken+"\r\n")
}

func (x troubleshootingFixture) dialRawBrowser(t *testing.T, b *browser, session uuid.UUID) (net.Conn, *bufio.Reader) {
	t.Helper()
	headers := "Origin: " + x.f.server.URL + "\r\n"
	for _, cookie := range b.client.Jar.Cookies(mustURL(x.f.server.URL)) {
		headers += "Cookie: " + cookie.String() + "\r\n"
	}
	return x.dialRawStream(t, x.prefix+"/troubleshooting-sessions/"+session.String()+"/stream", headers)
}

// An agent that stops answering, even inside a frame, neither delays the browser's orderly
// close nor keeps the session from being recorded as ended.
func TestTroubleshootingUnresponsiveAgentCannotHoldASessionOpen(t *testing.T) {
	for name, insideFrame := range map[string]bool{"silent": false, "inside a frame": true} {
		t.Run(name, func(t *testing.T) {
			x := relayFixture(t)
			x.f.apiServer.TerminalIdle = time.Second
			opened := x.open(t, x.owner, x.app, 1, 201)
			agent, reader := x.dialRawAgent(t, opened.Session.Id)
			browser := x.attachBrowser(t, x.owner, opened)
			started := time.Now()
			// The start signal in a three-byte frame, then the header of the relay's close frame.
			frames := make([]byte, 5)
			if _, err := io.ReadFull(reader, frames); err != nil {
				t.Fatal(err)
			}
			if frames[2] != terminal.Start || frames[3] != 0x88 {
				t.Fatalf("frames %x, want the start signal then a close", frames)
			}
			if insideFrame {
				// A final masked binary frame announcing 1000 bytes that never come.
				if _, err := agent.Write([]byte{0x82, 0x80 | 126, 0x03, 0xe8, 1, 2, 3, 4}); err != nil {
					t.Fatal(err)
				}
			}
			browser.drained(t)
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("browser stream ended after %s, waiting on the agent", elapsed)
			}
			if status := websocket.CloseStatus(browser.err); status != websocket.StatusNormalClosure {
				t.Fatalf("stream ended with %v, want a normal close", browser.err)
			}
			x.waitClosed(t, opened.Session.Id, "idle timeout")
		})
	}
}

// Reaching a refusal takes only an owner's or operator's session and the right Origin, with
// no valid ticket, so a peer that stops inside a frame after the refusal must not be able to
// keep the socket and its goroutine.
func TestTroubleshootingRefusedBrowserCannotHoldItsStream(t *testing.T) {
	x := relayFixture(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	conn, reader := x.dialRawBrowser(t, x.owner, opened.Session.Id)
	// A final masked text frame, whose zero mask leaves the payload as it is.
	ticket := "wrong-ticket"
	if _, err := conn.Write(append([]byte{0x81, 0x80 | byte(len(ticket)), 0, 0, 0, 0}, ticket...)); err != nil {
		t.Fatal(err)
	}
	// A close frame with status 1008, policy violation.
	refusal := append([]byte{0x88, 17, 0x03, 0xf0}, "session refused"...)
	got := make([]byte, len(refusal))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(refusal) {
		t.Fatalf("frame %x, want %x", got, refusal)
	}
	// A final masked binary frame announcing 1000 bytes that never come.
	if _, err := conn.Write([]byte{0x82, 0x80 | 126, 0x03, 0xe8, 1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	// The server's limit is 10 seconds.
	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("stream still held after the close limit: %v", err)
	}
}

// Nothing reads a browser stream during redemption, so a held workspace lock must not
// keep it for as long as the lock lasts. Giving up must leave the ticket usable.
func TestTroubleshootingRedemptionGivesUpWithoutUsingTheTicket(t *testing.T) {
	x := relayFixture(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	ctx := context.Background()
	lock, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT 1 FROM workspaces WHERE id=$1 FOR UPDATE", x.ws); err != nil {
		t.Fatal(err)
	}
	refused := x.attachBrowser(t, x.owner, opened)
	refused.drained(t)
	if status := websocket.CloseStatus(refused.err); status != websocket.StatusPolicyViolation {
		t.Fatalf("close status %d: %v", status, refused.err)
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	x.attachBrowser(t, x.owner, opened)
	agent.expect(t, []byte{terminal.Start})
}

func TestTroubleshootingStreamsRefuseAnyoneElse(t *testing.T) {
	x := relayFixture(t)
	operator := x.member(t, "operator@example.test", "operator")
	opened := x.open(t, x.owner, x.app, 1, 201)

	// Another server's credential cannot attach to this server's session.
	_, other, otherToken := enrollment(t, x.f, x.owner, x.ws, "deployment")
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/enroll", "", other, 200, nil)
	if conn, status := x.dialAgent(t, opened.Session.Id, otherToken); conn != nil || status != 404 {
		t.Fatalf("another server attached: %d", status)
	}
	if conn, status := x.dialAgent(t, opened.Session.Id, "not-a-credential"); conn != nil || status != 401 {
		t.Fatalf("bad credential attached: %d", status)
	}

	// The browser stream needs the right origin and a browser session before upgrading.
	if conn, status := x.dialBrowser(t, x.owner, opened.Session.Id, "https://attacker.example.test"); conn != nil || status != 403 {
		t.Fatalf("cross-origin stream: %d", status)
	}
	if conn, status := x.dialBrowser(t, x.f.browser(t), opened.Session.Id, x.f.server.URL); conn != nil || status != 401 {
		t.Fatalf("anonymous stream: %d", status)
	}

	refused := func(b *browser, ticket string) {
		t.Helper()
		conn, status := x.dialBrowser(t, b, opened.Session.Id, x.f.server.URL)
		if conn == nil {
			t.Fatalf("upgrade failed: %d", status)
		}
		if err := conn.Write(context.Background(), websocket.MessageText, []byte(ticket)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err := conn.Read(ctx)
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("ticket accepted: %v", err)
		}
	}
	refused(x.owner, "wrong-ticket")
	refused(operator, opened.Ticket) // another account
	secondSession := x.f.browser(t)
	secondSession.login("owner@example.test", x.f.password)
	refused(secondSession, opened.Ticket) // the same account in another browser

	// A ticket works once.
	agent, _ := x.dialAgent(t, opened.Session.Id, x.agentToken)
	x.attachBrowser(t, x.owner, opened)
	agent.expect(t, []byte{terminal.Start})
	refused(x.owner, opened.Ticket)
	x.audited(t, "session.refused.ticket", 2) // the owner's and the operator's, coalesced

}

// Whatever a session depends on, its end reaches an open stream promptly.
func TestTroubleshootingOpenSessionsEndWhenConditionsChange(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		reason string
		change func(t *testing.T, x troubleshootingFixture, owner, operator *browser, session uuid.UUID, operatorID uuid.UUID)
	}{
		"closed by its owner": {"closed by owner", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, session, _ uuid.UUID) {
			owner.call("POST", x.prefix+"/troubleshooting-sessions/"+session.String()+"/close", nil, 204, nil)
		}},
		"role removed": {"role changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, operatorID uuid.UUID) {
			owner.call("DELETE", x.prefix+"/members/"+operatorID.String(), nil, 204, nil)
		}},
		"setting off": {"environment setting changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, _ uuid.UUID) {
			owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: false, Generation: 1}, 200, nil)
		}},
		"browser session idled out": {"browser session ended", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, browserSessionExpiry("now()-interval '1 second'"), session); err != nil {
				t.Fatal(err)
			}
		}},
		"logged out": {"browser session ended", func(t *testing.T, x troubleshootingFixture, _, operator *browser, _, _ uuid.UUID) {
			operator.call("POST", "/api/v1/auth/logout", nil, 204, nil)
		}},
		"password changed": {"account security changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, _, operatorID uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE accounts SET auth_version=auth_version+1 WHERE id=$1", operatorID); err != nil {
				t.Fatal(err)
			}
		}},
		"server revoked": {"server credential changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET revoked=true WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"credential rotated": {"server credential changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET credential_hash=sha256('rotated') WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"controller replaced": {"server credential changed", func(t *testing.T, x troubleshootingFixture, _, _ *browser, session, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE servers SET controller_generation=controller_generation+1 WHERE id=(SELECT server_id FROM troubleshooting_sessions WHERE id=$1)", session); err != nil {
				t.Fatal(err)
			}
		}},
		"workspace now requires MFA": {"multi-factor authentication required", func(t *testing.T, x troubleshootingFixture, _, _ *browser, _, _ uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=true WHERE id=$1", x.ws); err != nil {
				t.Fatal(err)
			}
		}},
		"demoted under an owners-only restriction": {"environment setting changed", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, operatorID uuid.UUID) {
			if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_policies SET owners_only=true WHERE environment_id=$1", x.env); err != nil {
				t.Fatal(err)
			}
		}},
		"closed with all the others": {"closed by owner", func(t *testing.T, x troubleshootingFixture, owner, _ *browser, _, _ uuid.UUID) {
			owner.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 204, nil)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			x := relayFixture(t)
			operator := x.member(t, "operator@example.test", "operator")
			var operatorID uuid.UUID
			if err := x.f.st.Pool.QueryRow(ctx, "SELECT id FROM accounts WHERE email='operator@example.test'").Scan(&operatorID); err != nil {
				t.Fatal(err)
			}
			opened, agent, browser := x.connected(t, operator, 1)
			test.change(t, x, x.owner, operator, opened.Session.Id, operatorID)
			x.waitClosed(t, opened.Session.Id, test.reason)
			agent.toldWhy(t, test.reason)
			browser.drained(t)
		})
	}
}

// Confirming credentials renews the browser token and keeps the terminal; the binding
// moves with it, so the renewed session's logout still ends the terminal.
func TestTroubleshootingSurvivesReauthentication(t *testing.T) {
	x := relayFixture(t)
	opened, agent, browser := x.connected(t, x.owner, 1)
	var session api.Session
	x.owner.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": x.f.password}, 200, &session)
	x.owner.csrf = session.CsrfToken
	time.Sleep(time.Second + 500*time.Millisecond) // several reviews
	browser.send(t, append([]byte{terminal.Input}, "x"...))
	agent.expect(t, append([]byte{terminal.Input}, "x"...))
	x.owner.call("POST", "/api/v1/auth/logout", nil, 204, nil)
	x.waitClosed(t, opened.Session.Id, "browser session ended")
}

func TestTroubleshootingIdleLimitCountsOnlyTypedInput(t *testing.T) {
	x := relayFixture(t)
	x.f.apiServer.TerminalIdle = 3 * time.Second
	opened, agent, browser := x.connected(t, x.owner, 1)
	stopOutput := make(chan struct{})
	defer close(stopOutput)
	go func() {
		for {
			select {
			case <-stopOutput:
				return
			case <-time.After(200 * time.Millisecond):
				_ = agent.conn.Write(context.Background(), websocket.MessageBinary, append([]byte{terminal.Output}, "log\n"...))
			}
		}
	}()
	// Typing for longer than the limit keeps the session open.
	for range 5 {
		time.Sleep(time.Second)
		browser.send(t, append([]byte{terminal.Input}, "x"...))
	}
	var state string
	if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT state FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&state); err != nil || state == "closed" {
		t.Fatal("typing did not keep the session open", state, err)
	}
	// Output alone does not, and the browser hears before the limit ends the session.
	warned := false
	for deadline := time.After(20 * time.Second); ; {
		select {
		case frame, open := <-browser.frames:
			if !open {
				if !warned {
					t.Fatal("no warning before the limit")
				}
				x.waitClosed(t, opened.Session.Id, "idle timeout")
				agent.toldWhy(t, "idle timeout")
				return
			}
			warned = warned || frame[0] == terminal.Warning
		case <-deadline:
			t.Fatal("session outlived its idle limit")
		}
	}
}

// The relay's own timer ends a session at the deadline fixed when it attached.
func TestTroubleshootingDeadlineEndsTheRelay(t *testing.T) {
	x := relayFixture(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	if _, err := x.f.st.Pool.Exec(context.Background(), "UPDATE troubleshooting_sessions SET deadline_at=now()+interval '2 seconds' WHERE id=$1", opened.Session.Id); err != nil {
		t.Fatal(err)
	}
	// The browser attaches first, so the relay starts when the agent arrives.
	browser := x.attachBrowser(t, x.owner, opened)
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	agent.expect(t, []byte{terminal.Start})
	browser.drained(t)
	x.waitClosed(t, opened.Session.Id, "deadline")
	agent.toldWhy(t, "deadline")
}

func browserSessionExpiry(expiry string) string {
	return "UPDATE sessions b SET expiry=" + expiry + " FROM troubleshooting_sessions t WHERE t.id=$1 AND b.token=translate(rtrim(encode(t.browser_session_hash,'base64'),'='),'+/','-_')"
}

// Typing keeps the browser session alive, since a terminal sends no dashboard requests.
func TestTroubleshootingTypingRefreshesTheBrowserSession(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened, agent, browser := x.connected(t, x.owner, 1)
	if _, err := x.f.st.Pool.Exec(ctx, browserSessionExpiry("now()+interval '1 minute'"), opened.Session.Id); err != nil {
		t.Fatal(err)
	}
	browser.send(t, append([]byte{terminal.Input}, "x"...))
	agent.expect(t, append([]byte{terminal.Input}, "x"...))
	var refreshed bool
	for deadline := time.Now().Add(5 * time.Second); !refreshed && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(ctx, "SELECT b.expiry>now()+interval '10 minutes' FROM sessions b JOIN troubleshooting_sessions t ON b.token=translate(rtrim(encode(t.browser_session_hash,'base64'),'='),'+/','-_') WHERE t.id=$1", opened.Session.Id).Scan(&refreshed); err != nil {
			t.Fatal(err)
		}
	}
	if !refreshed {
		t.Fatal("typing did not refresh the browser session")
	}
}

// A stream past its allowance waits instead of buffering, in both directions.
func TestTroubleshootingRelayPacesBothDirections(t *testing.T) {
	x := relayFixture(t)
	_, agent, browser := x.connected(t, x.owner, 1)
	frame := func(kind byte) []byte {
		out := make([]byte, terminal.MaxFrame)
		out[0] = kind
		return out
	}
	started := time.Now()
	for range 8 { // 256 KiB at 64 KiB/s, less the one-frame burst
		browser.send(t, frame(terminal.Input))
	}
	for range 8 {
		<-agent.frames
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("input arrived in %s", elapsed)
	}
	started = time.Now()
	for range 64 { // 2 MiB at 1 MiB/s, less the one-frame burst
		agent.send(t, frame(terminal.Output))
	}
	for range 64 {
		<-browser.frames
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("output arrived in %s", elapsed)
	}
}

// Each side may send only its own frames, within the size limit.
func TestTroubleshootingRelayRefusesInvalidFrames(t *testing.T) {
	for name, test := range map[string]struct {
		reason string
		send   func(t *testing.T, agent, browser *terminalPeer)
	}{
		"browser sends output":  {"invalid frame", func(t *testing.T, _, browser *terminalPeer) { browser.send(t, append([]byte{terminal.Output}, "x"...)) }},
		"browser starts itself": {"invalid frame", func(t *testing.T, _, browser *terminalPeer) { browser.send(t, []byte{terminal.Start}) }},
		"agent sends input":     {"invalid frame", func(t *testing.T, agent, _ *terminalPeer) { agent.send(t, append([]byte{terminal.Input}, "x"...)) }},
		"agent forges warning":  {"invalid frame", func(t *testing.T, agent, _ *terminalPeer) { agent.send(t, terminal.WarningFrame(1)) }},
		"bad dimensions":        {"invalid frame", func(t *testing.T, _, browser *terminalPeer) { browser.send(t, terminal.ResizeFrame(0, 24)) }},
		"oversized frame":       {"browser stream lost", func(t *testing.T, _, browser *terminalPeer) { browser.send(t, make([]byte, terminal.MaxFrame+1)) }},
		"unknown refusal": {"agent refused: unknown", func(t *testing.T, agent, _ *terminalPeer) {
			agent.send(t, append([]byte{terminal.Refused}, "<script>"...))
		}},
		"known refusal": {"agent refused: no_shell", func(t *testing.T, agent, _ *terminalPeer) {
			agent.send(t, append([]byte{terminal.Refused}, terminal.RefusedNoShell...))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			x := relayFixture(t)
			opened, agent, browser := x.connected(t, x.owner, 1)
			test.send(t, agent, browser)
			x.waitClosed(t, opened.Session.Id, test.reason)
		})
	}
}

// A pending session wakes the poll but holds back no command, and only reaches agents
// that report support.
func TestTroubleshootingOffersStayOutOfTheCommandPath(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened := x.open(t, x.owner, x.app, 1, 201)
	server := opened.Session.ServerId
	offers, err := x.f.st.Queries.ListTroubleshootingOffers(ctx, db.ListTroubleshootingOffersParams{WorkspaceID: x.ws, ServerID: server})
	if err != nil || len(offers) != 1 {
		t.Fatal("offer missing", offers, err)
	}
	command := api.ExecutionCommand{Id: uuid.New(), Kind: "test", ServerId: server}
	raw, _ := stablejson.Marshal(command)
	if _, err = x.f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,1,$4,$5,'test','dispatched')`, command.Id, x.ws, server, uuid.NewString(), raw); err != nil {
		t.Fatal(err)
	}
	var poll api.AgentPoll
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/poll", x.agentToken, nil, 200, &poll)
	if poll.Command == nil || poll.TroubleshootingOffers == nil {
		t.Fatalf("poll must carry both: %+v", poll)
	}
	x.capabilities(t, func(*api.AgentCapabilities) {})
	if offers, err = x.f.st.Queries.ListTroubleshootingOffers(ctx, db.ListTroubleshootingOffersParams{WorkspaceID: x.ws, ServerID: server}); err != nil || len(offers) != 0 {
		t.Fatal("offered to an agent without support", offers, err)
	}
}

// A side that never arrives closes the session, and so does the startup sweep.
func TestTroubleshootingAbandonedSessionsClose(t *testing.T) {
	x := relayFixture(t)
	ctx := context.Background()
	opened := x.open(t, x.owner, x.app, 1, 201)
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()+interval '500 milliseconds' WHERE id=$1", opened.Session.Id); err != nil {
		t.Fatal(err)
	}
	agent, status := x.dialAgent(t, opened.Session.Id, x.agentToken)
	if agent == nil {
		t.Fatalf("agent stream refused: %d", status)
	}
	x.waitClosed(t, opened.Session.Id, "not attached")
	agent.toldWhy(t, "not attached")
	x.audited(t, "session.close", 1)

	left := x.open(t, x.owner, x.app, 2, 201)
	if err := x.f.execution.CloseAbandonedTroubleshootingSessions(ctx); err != nil {
		t.Fatal(err)
	}
	x.waitClosed(t, left.Session.Id, "management restart")
	x.audited(t, "session.close", 2)
}

// Both streams pass through an ordinary reverse proxy, with the browser's Origin still
// naming the public origin. This does not cover a proxy's idle or duration limits.
func TestTroubleshootingThroughAReverseProxy(t *testing.T) {
	x := relayFixture(t)
	target, err := url.Parse(x.f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(edge.Close)
	direct := x.f.server.URL
	via := "ws" + strings.TrimPrefix(edge.URL, "http")
	opened := x.open(t, x.owner, x.app, 1, 201)

	dial := func(path string, options *websocket.DialOptions) *terminalPeer {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, via+path, options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return readTerminal(conn)
	}
	agent := dial("/agent/v1/troubleshooting/"+opened.Session.Id.String()+"/stream", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + x.agentToken}}})
	cookies := x.owner.client.Jar.Cookies(target)
	header := http.Header{"Origin": {direct}}
	for _, cookie := range cookies {
		header.Add("Cookie", cookie.String())
	}
	browser := dial(x.prefix+"/troubleshooting-sessions/"+opened.Session.Id.String()+"/stream", &websocket.DialOptions{HTTPHeader: header})
	browser.send(t, []byte(opened.Ticket))
	agent.expect(t, []byte{terminal.Start})

	browser.send(t, append([]byte{terminal.Input}, "through\n"...))
	agent.expect(t, append([]byte{terminal.Input}, "through\n"...))
	agent.send(t, append([]byte{terminal.Output}, "proxied\n"...))
	browser.expect(t, append([]byte{terminal.Output}, "proxied\n"...))
	agent.send(t, []byte{terminal.Exit, 0})
	browser.expect(t, []byte{terminal.Exit, 0})
	x.waitClosed(t, opened.Session.Id, "shell exited")
}
