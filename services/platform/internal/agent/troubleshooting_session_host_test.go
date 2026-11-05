package agent

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type shellFixture struct {
	docker *client.Client
	client *Client
	plan   deployment.Plan
	app    uuid.UUID
	id     string
	conns  chan *websocket.Conn
}

// A stand-in for management: it hands each agent stream to the test.
func newShellFixture(t *testing.T) shellFixture {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	j, plan := testVisualJournal(t)
	if err = j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	conns := make(chan *websocket.Conn, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	cfg, _ := j.Config()
	cfg.Origin, cfg.Enrolled, cfg.Credential, cfg.ServerID = server.URL, true, strings.Repeat("c", 43), plan.ServerID
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err = j.SetTroubleshooting(true); err != nil {
		t.Fatal(err)
	}
	app := uuid.New()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(app), Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "300"}, Labels: visualLabels(plan, app)}, HostConfig: &container.HostConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	return shellFixture{docker, &Client{Journal: j, Engine: &Engine{Journal: j, Docker: docker}, HTTP: server.Client()}, plan, app, created.ID, conns}
}

func (f shellFixture) offer() api.AgentTroubleshootingOffer {
	return api.AgentTroubleshootingOffer{SessionId: uuid.New(), ActorId: uuid.New(), UnitId: f.plan.UnitID, ApplicationId: f.app, Instance: 1}
}

// run starts a session and returns the management side of its stream, started.
func (f shellFixture) run(t *testing.T, offer api.AgentTroubleshootingOffer) (*websocket.Conn, chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f.client.runTroubleshootingSession(context.Background(), offer) }()
	select {
	case conn := <-f.conns:
		t.Cleanup(func() { conn.CloseNow() })
		if err := conn.Write(context.Background(), websocket.MessageBinary, []byte{terminal.Start}); err != nil {
			t.Fatal(err)
		}
		return conn, done
	case err := <-done:
		t.Fatal("session ended before attaching", err)
		return nil, nil
	}
}

func readUntil(t *testing.T, conn *websocket.Conn, text string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seen := bytes.Buffer{}
	for !strings.Contains(seen.String(), text) {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("%q not seen in %q: %v", text, seen.String(), err)
		}
		if frame[0] != terminal.Output {
			t.Fatalf("unexpected frame %q", frame)
		}
		seen.Write(frame[1:])
	}
	return seen.String()
}

func typeInto(t *testing.T, conn *websocket.Conn, text string) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageBinary, append([]byte{terminal.Input}, text...)); err != nil {
		t.Fatal(err)
	}
}

func (f shellFixture) processes(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	created, err := f.docker.ExecCreate(ctx, f.id, client.ExecCreateOptions{Cmd: []string{"ps"}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := f.docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close()
	raw, _ := io.ReadAll(attached.Reader)
	return string(raw)
}

func (f shellFixture) waitGone(t *testing.T, name string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return // the daemon's processes are not visible from this machine
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !strings.Contains(f.processes(t), name) {
			return
		}
	}
	t.Fatalf("%s survived the session:\n%s", name, f.processes(t))
}

func TestNativeTroubleshootingSession(t *testing.T) {
	f := newShellFixture(t)
	conn, done := f.run(t, f.offer())

	// The shell runs in the container, at the root, with a terminal and only TERM added.
	if err := conn.Write(context.Background(), websocket.MessageBinary, terminal.ResizeFrame(100, 40)); err != nil {
		t.Fatal(err)
	}
	// Docker's default detach keys would hold Ctrl-P and end the stream at Ctrl-Q.
	typeInto(t, conn, "\x10\x11stty size; pwd; echo TERM=$TERM; echo do''ne\n")
	output := readUntil(t, conn, "done")
	for _, want := range []string{"40 100", "\n/\r", "TERM=xterm-256color"} {
		if !strings.Contains(output, want) {
			t.Fatalf("%q missing from %q", want, output)
		}
	}

	// Background jobs sit in their own process groups but the shell's Linux session.
	typeInto(t, conn, "sleep 4242 &\nsleep 4343 &\necho st''arted\n")
	readUntil(t, conn, "started")
	if !strings.Contains(f.processes(t), "sleep 4242") {
		t.Fatal("background job did not start")
	}
	shells, err := f.client.Journal.troubleshootingShells()
	if err != nil || len(shells) != 1 {
		t.Fatal("shell not journaled", shells, err)
	}
	conn.CloseNow()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	f.waitGone(t, "sleep 4242")
	f.waitGone(t, "sleep 4343")
	if shells, err = f.client.Journal.troubleshootingShells(); err != nil || len(shells) != 0 {
		t.Fatal("journal entry left behind", shells, err)
	}
}

// A job that outlives its shell is still in the shell's session, and an agent stopping
// mid-session ends everything in it.
func TestNativeTroubleshootingEndsSurvivors(t *testing.T) {
	f := newShellFixture(t)
	conn, done := f.run(t, f.offer())
	typeInto(t, conn, "nohup sleep 4646 >/dev/null 2>&1 &\nsleep 1; exit 0\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.waitGone(t, "sleep 4646")

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopped := make(chan error, 1)
	go func() { stopped <- f.client.runTroubleshootingSession(ctx, f.offer()) }()
	conn = <-f.conns
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte{terminal.Start}); err != nil {
		t.Fatal(err)
	}
	typeInto(t, conn, "sleep 4747 &\necho st''arted\n")
	readUntil(t, conn, "started")
	stop()
	<-stopped
	f.waitGone(t, "sleep 4747")
	if shells, err := f.client.Journal.troubleshootingShells(); err != nil || len(shells) != 0 {
		t.Fatal("journal entry left behind", shells, err)
	}
}

// A shell that stops reading must not look like a lost stream, and input it cannot take
// ends the session instead of reaching it with a gap.
func TestNativeTroubleshootingStalledInput(t *testing.T) {
	f := newShellFixture(t)
	conn, done := f.run(t, f.offer())
	typeInto(t, conn, "sleep 30\n")
	// This reader also handles pongs, so it must keep draining however much the terminal echoes.
	want := string(append([]byte{terminal.Refused}, terminal.RefusedInputOverflow...))
	refused := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		seen := false
		for {
			_, frame, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if !seen && string(frame) == want {
				seen = true
				close(refused)
			}
		}
	}()
	time.Sleep(time.Second)
	// Complete lines fill the terminal's buffer, where text with no newline would be discarded.
	paste := make([]byte, terminal.MaxFrame)
	paste[0] = terminal.Input
	for i := 1; i < len(paste); i++ {
		paste[i] = 'x'
	}
	paste[len(paste)-1] = '\n'
	send := func(count int) {
		t.Helper()
		for range count {
			if err := conn.Write(context.Background(), websocket.MessageBinary, paste); err != nil {
				return // the agent has ended the session
			}
		}
	}
	send(troubleshootingInputQueue) // more than the buffers on the way, within the queue
	ping, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conn.Ping(ping); err != nil {
		t.Fatal("agent stopped answering while the shell was not reading", err)
	}
	send(4 * troubleshootingInputQueue)
	select {
	case <-refused:
	case <-closed:
		select {
		case <-refused:
		default:
			t.Fatal("stream closed with no overflow refusal")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no overflow refusal")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The probe's answer reaches the person when an image has no shell.
// captureTrail collects what the agent logs for the rest of the test.
func captureTrail(t *testing.T) *bytes.Buffer {
	t.Helper()
	trail := &bytes.Buffer{}
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(trail, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return trail
}

// execute runs a command in the fixture's container and waits for it to succeed.
func (f shellFixture) execute(t *testing.T, command ...string) {
	t.Helper()
	ctx := context.Background()
	created, err := f.docker.ExecCreate(ctx, f.id, client.ExecCreateOptions{Cmd: command})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.docker.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		state, err := f.docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !state.Running && state.ExitCode == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not succeed", command, state)
		}
	}
}

// Bash is preferred only if it runs. Finding that out must not start a missing executable:
// Docker can stall the next exec after one that failed to start.
func TestNativeTroubleshootingShellChoice(t *testing.T) {
	f := newShellFixture(t)
	trail := captureTrail(t)
	for _, step := range []struct{ name, script, want string }{
		{"no bash", "true", "/bin/sh"},
		{"working bash", `printf '#!/bin/sh\nexec /bin/sh "$@"\n' >/bin/bash; chmod +x /bin/bash`, "/bin/bash"},
		{"unusable bash", `printf '#!/nonexistent\n' >/bin/bash`, "/bin/sh"},
		{"no sh", `printf '#!/bin/ash\nexec /bin/ash "$@"\n' >/bin/bash; rm /bin/sh`, "/bin/bash"},
	} {
		f.execute(t, "/bin/sh", "-c", step.script)
		got, err := f.client.Engine.findShell(context.Background(), f.id)
		if err != nil || got != step.want {
			t.Fatalf("%s: shell %q err %v, want %q", step.name, got, err, step.want)
		}
	}
	if strings.Contains(trail.String(), "did not start") {
		t.Fatalf("a probe started a missing executable: %s", trail.String())
	}
}

func TestNativeTroubleshootingNoShell(t *testing.T) {
	f := newShellFixture(t)
	ctx := context.Background()
	f.execute(t, "/bin/rm", "-f", "/bin/sh", "/bin/ash", "/bin/bash")
	conn, _ := f.run(t, f.offer())
	read, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, frame, err := conn.Read(read)
	if err != nil || frame[0] != terminal.Refused || string(frame[1:]) != terminal.RefusedNoShell {
		t.Fatalf("frame %q err %v, want the no_shell refusal", frame, err)
	}
}

// A failed stream is not retried on every poll: the offer's ticket is about to expire.
func TestTroubleshootingOfferIsAttemptedOnce(t *testing.T) {
	j, plan := testVisualJournal(t)
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin, cfg.Enrolled, cfg.Credential, cfg.ServerID = server.URL, true, strings.Repeat("c", 43), plan.ServerID
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, Engine: &Engine{Journal: j}, HTTP: server.Client()}
	offers := []api.AgentTroubleshootingOffer{{SessionId: uuid.New(), UnitId: plan.UnitID, ApplicationId: uuid.New(), Instance: 1}}
	for range 3 {
		c.offerTroubleshootingSessions(context.Background(), offers)
		c.terminalSessions.Wait()
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("%d stream attempts, want 1", got)
	}
}

func TestNativeTroubleshootingShellExit(t *testing.T) {
	f := newShellFixture(t)
	trail := captureTrail(t)
	conn, done := f.run(t, f.offer())
	typeInto(t, conn, "exit 7\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame[0] == terminal.Exit {
			if len(frame) != 2 || frame[1] != 7 {
				t.Fatalf("exit frame %v", frame)
			}
			break
		}
	}
	// The peer's close reply must not turn a clean exit into a lost stream on the host's trail.
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trail.String(), `reason="shell exited"`) {
		t.Fatalf("host trail: %s", trail.String())
	}
}

// The agent's own limit ends a session even if management never does.
func TestNativeTroubleshootingIdleLimit(t *testing.T) {
	f := newShellFixture(t)
	f.client.Engine.TroubleshootingIdle = 2 * time.Second
	conn, done := f.run(t, f.offer())
	typeInto(t, conn, "sleep 4444 &\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("agent did not enforce the idle limit")
	}
	f.waitGone(t, "sleep 4444")
}

// Withdrawn consent and an ineligible target are refused over the stream.
func TestNativeTroubleshootingRefusals(t *testing.T) {
	f := newShellFixture(t)
	read := func(conn *websocket.Conn, want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, frame, err := conn.Read(ctx)
		if err != nil || frame[0] != terminal.Refused || string(frame[1:]) != want {
			t.Fatalf("frame %q err %v, want refusal %q", frame, err, want)
		}
	}
	missing := f.offer()
	missing.Instance = 2
	conn, _ := f.run(t, missing)
	read(conn, terminal.RefusedIneligible)
	if err := f.client.Journal.SetTroubleshooting(false); err != nil {
		t.Fatal(err)
	}
	conn, _ = f.run(t, f.offer())
	read(conn, terminal.RefusedOptIn)
}

// A restarted agent ends a shell a crash left behind, and nothing else.
func TestNativeTroubleshootingCrashCleanup(t *testing.T) {
	f := newShellFixture(t)
	if runtime.GOOS != "linux" {
		t.Skip("the daemon's processes are not visible from this machine")
	}
	ctx := context.Background()
	start := func() (string, shellIdentity) {
		t.Helper()
		created, err := f.docker.ExecCreate(ctx, f.id, client.ExecCreateOptions{Cmd: []string{"sleep", "4545"}, TTY: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.docker.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true, TTY: true}); err != nil {
			t.Fatal(err)
		}
		identity, err := f.client.Engine.startedShell(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID, identity
	}
	clean := func(entry troubleshootingShell) {
		t.Helper()
		entry.Session, entry.ContainerID = uuid.New(), f.id
		if err := f.client.Journal.saveTroubleshootingShell(entry); err != nil {
			t.Fatal(err)
		}
		f.client.Engine.EndJournaledShells(ctx)
		if shells, err := f.client.Journal.troubleshootingShells(); err != nil || len(shells) != 0 {
			t.Fatal("journal entry left behind", shells, err)
		}
	}
	exec, identity := start()
	host, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	// An identity that no longer names the shell must not kill, whatever the daemon says.
	for name, stale := range map[string]func(*troubleshootingShell){
		"reused PID":     func(e *troubleshootingShell) { e.Identity.StartTime++ },
		"other boot":     func(e *troubleshootingShell) { e.Identity.Boot = "another-boot" },
		"host namespace": func(e *troubleshootingShell) { e.Identity.PIDNS = host },
		"unknown exec":   func(e *troubleshootingShell) { e.Identity.StartTime++; e.ExecID = "unknown" },
	} {
		entry := troubleshootingShell{ExecID: exec, Identity: identity}
		stale(&entry)
		clean(entry)
		if !strings.Contains(f.processes(t), "sleep 4545") {
			t.Fatalf("%s: killed a process that only shares a journaled PID", name)
		}
	}
	// A matching identity needs nothing from the daemon, which may be restarting.
	clean(troubleshootingShell{ExecID: "unknown", Identity: identity})
	f.waitGone(t, "sleep 4545")
	// An entry written before the shell was identified is resolved through its exec.
	exec, _ = start()
	clean(troubleshootingShell{ExecID: exec})
	f.waitGone(t, "sleep 4545")
	// The agent ends leftovers when it starts, before doing anything else.
	exec, identity = start()
	if err = f.client.Journal.saveTroubleshootingShell(troubleshootingShell{Session: uuid.New(), ContainerID: f.id, ExecID: exec, Identity: identity}); err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	_ = f.client.Run(stopped)
	f.waitGone(t, "sleep 4545")
}
