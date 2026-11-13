package agent

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// execDaemon answers the exec API as the daemon does: an exec it knows is described, one
// it has forgotten is not found, and a resize succeeds.
func execDaemon(t *testing.T, execs map[string]string) *client.Client {
	t.Helper()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path[strings.Index(r.URL.Path, "/exec/")+len("/exec/"):]
		id, action, _ := strings.Cut(path, "/")
		state, known := execs[id]
		switch {
		case !known:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such exec instance: ` + id + `"}`))
		case r.Method == http.MethodGet && action == "json":
			_, _ = w.Write([]byte(state))
		case r.Method == http.MethodPost && action == "resize":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected daemon call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(daemon.Close)
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return docker
}

// trailFor returns the lines the agent logged about one session.
func trailFor(trail string, session uuid.UUID) []string {
	lines := []string{}
	for line := range strings.Lines(trail) {
		if strings.Contains(line, session.String()) {
			lines = append(lines, line)
		}
	}
	return lines
}

// After a restart the host's trail says what cleanup did. A shell the daemon has forgotten
// cannot be traced, so nothing is said to have ended, and one that had already exited left
// nothing to end.
func TestTroubleshootingCleanupReportsWhatItDid(t *testing.T) {
	j, _ := testVisualJournal(t)
	e := &Engine{Journal: j, Docker: execDaemon(t, map[string]string{"exited": `{"ID":"exited","Running":false,"ExitCode":0,"Pid":4242}`})}
	forgotten, exited := uuid.New(), uuid.New()
	for _, shell := range []troubleshootingShell{{Session: forgotten, ContainerID: "container", ExecID: "forgotten"}, {Session: exited, ContainerID: "container", ExecID: "exited"}} {
		if err := j.saveTroubleshootingShell(shell); err != nil {
			t.Fatal(err)
		}
	}
	trail := captureTrail(t)
	e.EndJournaledShells(context.Background())
	if shells, err := j.troubleshootingShells(); err != nil || len(shells) != 0 {
		t.Fatal("journal entries left behind", shells, err)
	}
	if lines := trailFor(trail.String(), forgotten); len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "cannot be identified") {
		t.Fatalf("a forgotten shell's trail: %q", lines)
	}
	if lines := trailFor(trail.String(), exited); len(lines) != 1 || !strings.Contains(lines[0], "found nothing left to end") {
		t.Fatalf("an exited shell's trail: %q", lines)
	}
}

// shellStream stands in for the daemon's attached exec: a buffered TCP stream, of which the
// test holds the shell's end.
func shellStream(t *testing.T) (client.ExecAttachResult, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	agent, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	shell := <-accepted
	if shell == nil {
		t.Fatal("no shell stream")
	}
	t.Cleanup(func() {
		agent.Close()
		shell.Close()
	})
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: agent, Reader: bufio.NewReader(agent)}}, shell
}

// managementStream connects an agent stream to a stand-in for management, and returns the
// agent's end and management's.
func managementStream(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agent, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	management := <-accepted
	t.Cleanup(func() {
		agent.CloseNow()
		management.CloseNow()
	})
	return agent, management
}

func closedBy(reason string) func(*websocket.Conn) {
	return func(conn *websocket.Conn) { terminal.Close(conn, websocket.StatusNormalClosure, reason) }
}

// The host's trail records why management ended a session: at its idle limit or deadline,
// closed by an owner or when it could no longer run. Only a stream that really broke is a
// lost stream. A shell printing as the close arrives makes a write fail on the closed
// stream, and that must not hide the reason either.
func TestTroubleshootingTrailNamesManagementsEnding(t *testing.T) {
	for name, test := range map[string]struct {
		end   func(*websocket.Conn)
		busy  bool
		want  string
		times int
	}{
		"idle limit":                      {closedBy("idle timeout"), false, "ended by management: idle timeout", 1},
		"deadline while the shell prints": {closedBy("deadline"), true, "ended by management: deadline", 20},
		"closed by an owner":              {closedBy("closed by owner"), false, "ended by management: closed by owner", 1},
		"no longer allowed":               {closedBy("role changed"), false, "ended by management: role changed", 1},
		"management without reasons":      {closedBy(""), false, "ended by management", 1},
		"reason not printable":            {closedBy("idle\x1b[2J"), false, "ended by management", 1},
		"stream lost":                     {func(conn *websocket.Conn) { conn.CloseNow() }, false, "stream lost", 1},
		// Caddy says this to both sides as it reloads its configuration.
		"a proxy going away":         {func(conn *websocket.Conn) { terminal.Close(conn, websocket.StatusGoingAway, "") }, false, "stream lost", 1},
		"stream lost while printing": {func(conn *websocket.Conn) { conn.CloseNow() }, true, "stream lost", 5},
	} {
		t.Run(name, func(t *testing.T) {
			e := &Engine{Docker: execDaemon(t, map[string]string{"exec": `{"ID":"exec","Running":true,"Pid":0}`})}
			for range test.times {
				agent, management := managementStream(t)
				attached, shell := shellStream(t)
				if test.busy {
					go func() {
						for {
							if _, err := shell.Write([]byte("log line\r\n")); err != nil {
								return
							}
						}
					}()
				}
				ended := make(chan string, 1)
				go func() { ended <- e.pumpShell(context.Background(), agent, "exec", attached, nil) }()
				// The session is under way: input reaches the shell, and any output arrives.
				if err := management.Write(context.Background(), websocket.MessageBinary, append([]byte{terminal.Input}, "x"...)); err != nil {
					t.Fatal(err)
				}
				if _, err := shell.Read(make([]byte, 1)); err != nil {
					t.Fatal(err)
				}
				if test.busy {
					read, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_, _, err := management.Read(read)
					cancel()
					if err != nil {
						t.Fatal(err)
					}
				}
				test.end(management)
				select {
				case got := <-ended:
					if got != test.want {
						t.Fatalf("session ended with %q, want %q", got, test.want)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("session did not end")
				}
			}
		})
	}
}

// Management can end a session before its shell starts, for example when the person left
// while the agent was connecting. The trail records that and why, not a failure.
func TestTroubleshootingEndBeforeStartIsJournaled(t *testing.T) {
	j, plan := testVisualJournal(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		terminal.Close(conn, websocket.StatusPolicyViolation, "browser stream lost")
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin, cfg.Enrolled, cfg.Credential, cfg.ServerID = server.URL, true, strings.Repeat("c", 43), plan.ServerID
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// Consent is off, so the agent refuses without asking Docker, once management starts it.
	if err := j.SetTroubleshooting(false); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, Engine: &Engine{Journal: j}, HTTP: server.Client()}
	trail := captureTrail(t)
	offer := api.AgentTroubleshootingOffer{SessionId: uuid.New(), ActorId: uuid.New(), UnitId: plan.UnitID, ApplicationId: uuid.New(), Instance: 1}
	if err := c.runTroubleshootingSession(context.Background(), offer); err != nil {
		t.Fatal(err)
	}
	if lines := trailFor(trail.String(), offer.SessionId); len(lines) != 1 || !strings.Contains(lines[0], `msg="terminal session ended before its shell started"`) || !strings.Contains(lines[0], `reason="ended by management: browser stream lost"`) {
		t.Fatalf("trail: %q", lines)
	}
}

// A deployment that makes the service elevated ends a running shell, and the person is told
// the container is no longer eligible.
func TestTroubleshootingShellEndsWhenItsServiceBecomesElevated(t *testing.T) {
	e := &Engine{Docker: execDaemon(t, map[string]string{"exec": `{"ID":"exec","Running":true,"Pid":0}`})}
	agent, management := managementStream(t)
	attached, _ := shellStream(t)
	end := make(chan struct{})
	ended := make(chan string, 1)
	go func() { ended <- e.pumpShell(context.Background(), agent, "exec", attached, end) }()
	close(end)
	read, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, frame, err := management.Read(read)
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]byte{terminal.Refused}, terminal.RefusedIneligible...); string(frame) != string(want) {
		t.Fatalf("last frame %q, want %q", frame, want)
	}
	go management.Read(read) // answers the closing handshake
	select {
	case got := <-ended:
		if got != "service became elevated" {
			t.Fatalf("session ended with %q", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("session did not end")
	}
}
