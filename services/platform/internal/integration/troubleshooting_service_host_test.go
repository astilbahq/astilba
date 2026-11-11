package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// nativeRoot makes a private directory the unit can execute from. GitHub's AWS-hosted
// runners mount /run noexec, and the unit's PrivateTmp and ProtectHome hide /tmp and home.
func nativeRoot(t *testing.T, tag string) string {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native agent test requires root on disposable Linux")
	}
	root := filepath.Join("/var/lib", tag+"-"+uuid.NewString())
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func buildNativeAgent(t *testing.T, path, version string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+version, "-o", path, "./cmd/agent")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("agent build: %s %v", out, err)
	}
}

// installAgentUnit installs the production unit under a unique name, changing only the
// paths of this disposable installation, and removes it afterwards.
func installAgentUnit(t *testing.T, root, binary, state, caPath string, run ...string) (string, func(...string) []byte) {
	t.Helper()
	unit := filepath.Base(root) + ".service"
	unitPath := filepath.Join("/run/systemd/system", unit)
	systemctl := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("systemctl %s: %s %v", args, out, err)
		}
		return out
	}
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "stop", unit).Run()
		_ = os.Remove(unitPath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	})
	template, err := os.ReadFile("../../host/astilba-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(template), "ExecStart=/usr/local/bin/astilba-agent run", "ExecStart="+binary+" run --state "+state+" --docker "+os.Getenv("PLAT_APP_DOCKER_HOST")+" --executable "+binary+" "+strings.Join(run, " "))
	text = strings.ReplaceAll(text, "ReadWritePaths=/var/lib/astilba-agent /usr/local/bin", "ReadWritePaths="+root)
	text = strings.Replace(text, "[Service]", "[Service]\nEnvironment=SSL_CERT_FILE="+caPath, 1)
	if err = os.WriteFile(unitPath, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("systemd-analyze", "verify", unitPath).CombinedOutput(); err != nil {
		t.Fatalf("unit verification: %s %v", out, err)
	}
	systemctl("daemon-reload")
	return unit, systemctl
}

// serviceAgent is the production agent binary, enrolled by its own commands and running
// under the production unit, with a real container for it to enter.
type serviceAgent struct {
	server    uuid.UUID
	unit      string
	systemctl func(...string) []byte
	docker    *client.Client
	container string
}

// startServiceAgent enrolls an agent with the management origin the test reaches, moves
// the fixture's deployment unit to it and opts in with the documented command.
func (x troubleshootingFixture) startServiceAgent(t *testing.T, owner *browser, binary, origin, caPath string) *serviceAgent {
	t.Helper()
	ctx := context.Background()
	root := nativeRoot(t, "astilba-p17")
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "release.pem")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	var enrollment api.Enrollment
	owner.call("POST", x.prefix+"/servers", api.CreateServer{Name: "Native host", Role: "deployment"}, 201, &enrollment)
	state := filepath.Join(root, "state")
	dockerHost := os.Getenv("PLAT_APP_DOCKER_HOST")
	enroll := exec.Command(binary, "enroll", "--state", state, "--management", origin, "--server", enrollment.Server.Id.String(), "--release-key", keyPath, "--docker", dockerHost)
	enroll.Env = append(os.Environ(), "SSL_CERT_FILE="+caPath)
	enroll.Stdin = strings.NewReader(enrollment.Token)
	if out, err := enroll.CombinedOutput(); err != nil {
		t.Fatalf("enrollment: %s %v", out, err)
	}
	if out, err := exec.Command(binary, "troubleshooting-enable", "--state", state, "--docker", dockerHost).CombinedOutput(); err != nil {
		t.Fatalf("opt-in: %s %v", out, err)
	}

	plan := deployment.Plan{EnvironmentID: x.env, ServerID: enrollment.Server.Id}
	if err = x.f.st.Pool.QueryRow(ctx, "UPDATE deployment_units SET server_id=$3 WHERE workspace_id=$1 AND environment_id=$2 AND primary_unit RETURNING id", x.ws, x.env, enrollment.Server.Id).Scan(&plan.UnitID); err != nil {
		t.Fatal(err)
	}
	journal, err := agent.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	err = journal.RecordUnitPlan(plan)
	journal.Close()
	if err != nil {
		t.Fatal(err)
	}

	docker, err := client.New(client.WithHost(dockerHost))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(x.app), Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "86400"}, Labels: map[string]string{"com.astilba.server": plan.ServerID.String(), "com.astilba.environment": x.env.String(), "com.astilba.unit": plan.UnitID.String(), "com.astilba.resource": x.app.String(), "com.astilba.kind": "visual-v1"}}, HostConfig: &container.HostConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	// The fixture's Compose plugin is outside the unit's PATH, and an agent without it reports the host unready.
	config := filepath.Join(root, "docker-config")
	if err = os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "docker-cli")
	if err = os.WriteFile(cli, []byte("#!/bin/sh\nexport DOCKER_CONFIG='"+config+"'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	unit, systemctl := installAgentUnit(t, root, binary, state, caPath, "--docker-cli", cli)
	systemctl("start", unit)
	a := &serviceAgent{plan.ServerID, unit, systemctl, docker, created.ID}
	// A session that ends early shows only as input that never ran, without these.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("session states: %v", x.closeReasons(t))
			t.Logf("agent journal:\n%s", a.journal(t))
		}
	})
	x.awaitSessions(t, a)
	return a
}

// awaitSessions waits for an agent's report that it takes sessions. An agent reports the
// extension only after a response shows the controller accepts it, so its first heartbeat
// after a restart would make an earlier report misleading.
func (x troubleshootingFixture) awaitSessions(t *testing.T, a *serviceAgent) {
	t.Helper()
	reported := false
	for deadline := time.Now().Add(60 * time.Second); !reported && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT COALESCE(capabilities->>'troubleshooting'='true' AND capabilities->>'troubleshootingEnabled'='true',false) FROM servers WHERE id=$1", a.server).Scan(&reported); err != nil {
			t.Fatal(err)
		}
	}
	if !reported {
		t.Fatalf("agent never reported sessions: %s", a.journal(t))
	}
}

// forgetSessions discards the report of an agent that is about to restart.
func (x troubleshootingFixture) forgetSessions(t *testing.T, a *serviceAgent) {
	t.Helper()
	if _, err := x.f.st.Pool.Exec(context.Background(), "UPDATE servers SET capabilities=capabilities-'troubleshooting' WHERE id=$1", a.server); err != nil {
		t.Fatal(err)
	}
}

func (a *serviceAgent) journal(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("journalctl", "--unit", a.unit, "--output", "cat", "--no-pager").CombinedOutput()
	if err != nil {
		t.Fatalf("journalctl: %s %v", out, err)
	}
	return string(out)
}

// journaled waits for one line naming everything given, which is what the host owner's
// trail must hold. Journald can lag the process that wrote the record.
func (a *serviceAgent) journaled(t *testing.T, parts ...string) {
	t.Helper()
	text := ""
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		text = a.journal(t)
	lines:
		for line := range strings.SplitSeq(text, "\n") {
			for _, part := range parts {
				if !strings.Contains(line, part) {
					continue lines
				}
			}
			return
		}
	}
	t.Fatalf("no journal line with %q in:\n%s", parts, text)
}

// recordedSession checks the trail the design promises for one finished session.
func (a *serviceAgent) recordedSession(t *testing.T, session, actor uuid.UUID, reason string) {
	t.Helper()
	identity := []string{"session=" + session.String(), "actor=" + actor.String(), "container=" + a.container}
	a.journaled(t, append([]string{"terminal session started"}, identity...)...)
	a.journaled(t, append([]string{"terminal session ended", "reason=" + reason}, identity...)...)
}

func (a *serviceAgent) running(t *testing.T, command string) bool {
	t.Helper()
	top, err := a.docker.ContainerTop(context.Background(), a.container, client.ContainerTopOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range top.Processes {
		if strings.Contains(strings.Join(process, " "), command) {
			return true
		}
	}
	return false
}

func (a *serviceAgent) waitRunning(t *testing.T, command string, want bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if a.running(t, command) == want {
			return
		}
	}
	t.Fatalf("%q running should be %v", command, want)
}

// jobEnded waits for a session's job to go. The next session opens straight away, while
// the agent is still finishing this one, as a person's reopening can.
func (a *serviceAgent) jobEnded(t *testing.T, job string) {
	t.Helper()
	a.waitRunning(t, job, false)
}

// waitOutput reads a stream until its output holds the text, and returns what it read.
// Limit warnings may arrive at any point and are not the frames under test.
func (p *terminalPeer) waitOutput(t *testing.T, text string) string {
	t.Helper()
	output := strings.Builder{}
	for deadline := time.After(30 * time.Second); ; {
		select {
		case frame, open := <-p.frames:
			if !open {
				t.Fatalf("stream ended (%v) before %q: %q", p.err, text, output.String())
			}
			switch frame[0] {
			case terminal.Output:
				output.Write(frame[1:])
				if strings.Contains(output.String(), text) {
					return output.String()
				}
			case terminal.Warning:
			default:
				t.Fatalf("frame %v before %q in %q", frame, text, output.String())
			}
		case <-deadline:
			t.Fatalf("no %q in %q", text, output.String())
		}
	}
}

// waitExit reads a stream to its Exit frame and the close handshake after it.
func (p *terminalPeer) waitExit(t *testing.T, code byte) {
	t.Helper()
	for deadline := time.After(30 * time.Second); ; {
		select {
		case frame, open := <-p.frames:
			if !open {
				t.Fatalf("stream ended without an exit frame: %v", p.err)
			}
			if frame[0] == terminal.Exit {
				if len(frame) != 2 || frame[1] != code {
					t.Fatalf("exit frame %v, want code %d", frame, code)
				}
				p.drained(t)
				if websocket.CloseStatus(p.err) != websocket.StatusNormalClosure {
					t.Fatalf("stream ended with %v, want a normal close", p.err)
				}
				return
			}
		case <-deadline:
			t.Fatal("no exit frame")
		}
	}
}

// closedWithin reads a stream until it closes and reports whether a limit warning came first.
func (p *terminalPeer) closedWithin(t *testing.T, limit time.Duration) bool {
	t.Helper()
	warned := false
	for deadline := time.After(limit); ; {
		select {
		case frame, open := <-p.frames:
			if !open {
				return warned
			}
			warned = warned || frame[0] == terminal.Warning
		case <-deadline:
			t.Fatalf("stream still open after %s", limit)
		}
	}
}

// startJob runs a background job from a shell, which job control places in its own
// process group, and waits for it, so cleanup has to reach the shell's whole Linux session.
func (a *serviceAgent) startJob(t *testing.T, browser *terminalPeer, seconds int) string {
	t.Helper()
	job := fmt.Sprintf("sleep %d", seconds)
	browser.send(t, append([]byte{terminal.Input}, job+" &\n"...))
	a.waitRunning(t, job, true)
	return job
}

// A session under the production unit: the sandbox allows exec and process cleanup, the
// host owner's journal records it without its content, and no shell outlives a restart
// or a crash of the agent.
func TestNativeTroubleshootingService(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires an explicitly disposable Ubuntu systemd host")
	}
	root := nativeRoot(t, "astilba-p17-build")
	binary := filepath.Join(root, "agent")
	buildNativeAgent(t, binary, "fixture.1")
	x := relayFixture(t)
	tlsServer := httptest.NewTLSServer(x.f.server.Config.Handler)
	t.Cleanup(tlsServer.Close)
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	a := x.startServiceAgent(t, x.owner, binary, tlsServer.URL, caPath)

	// Sessions would still pass without these, so the sessions below prove nothing about
	// the sandbox unless it is known to be on.
	sandbox := string(a.systemctl("show", a.unit, "--property=NoNewPrivileges,ProtectHome,ProtectSystem,PrivateTmp,RestrictAddressFamilies,UMask"))
	for _, want := range []string{"NoNewPrivileges=yes", "ProtectHome=yes", "ProtectSystem=strict", "PrivateTmp=yes", "RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_UNIX", "UMask=0077"} {
		if !strings.Contains(sandbox, want+"\n") {
			t.Fatalf("unit in force lacks %s:\n%s", want, sandbox)
		}
	}

	opened := x.open(t, x.owner, x.app, 1, 201)
	browser := x.attachBrowser(t, x.owner, opened)
	browser.send(t, terminal.ResizeFrame(100, 30))
	browser.send(t, append([]byte{terminal.Input}, "stty size; echo pri''vate''text\n"...))
	if output := browser.waitOutput(t, "privatetext"); !strings.Contains(output, "30 100") {
		t.Fatalf("the resize did not reach the shell: %q", output)
	}
	job := a.startJob(t, browser, 4321)
	browser.send(t, append([]byte{terminal.Input}, "exit 3\n"...))
	browser.waitExit(t, 3)
	x.waitClosed(t, opened.Session.Id, "shell exited")
	a.jobEnded(t, job)
	a.recordedSession(t, opened.Session.Id, x.f.owner, `"shell exited"`)
	for _, content := range []string{"privatetext", "pri''vate"} { // what the shell printed and what was typed
		if strings.Contains(a.journal(t), content) {
			t.Fatal("terminal content reached the journal")
		}
	}

	// Stopping the service ends the shell before the agent exits, so nothing waits for the next start.
	opened = x.open(t, x.owner, x.app, 1, 201)
	browser = x.attachBrowser(t, x.owner, opened)
	job = a.startJob(t, browser, 4322)
	a.systemctl("stop", a.unit)
	if a.running(t, job) {
		t.Fatal("a job outlived the stopped service")
	}
	browser.drained(t)
	x.waitClosed(t, opened.Session.Id, "agent stream lost")
	a.recordedSession(t, opened.Session.Id, x.f.owner, `"agent stopped"`)
	x.forgetSessions(t, a)
	a.systemctl("start", a.unit)
	x.awaitSessions(t, a)

	// After a crash nothing is left to clean up until systemd restarts the agent, which
	// finds the shell in its journal.
	opened = x.open(t, x.owner, x.app, 1, 201)
	browser = x.attachBrowser(t, x.owner, opened)
	job = a.startJob(t, browser, 4323)
	a.journaled(t, "terminal session started", "session="+opened.Session.Id.String())
	a.systemctl("kill", "--kill-whom=main", "--signal=SIGKILL", a.unit)
	if !a.running(t, job) {
		t.Fatal("nothing should have ended the job before the agent restarted")
	}
	x.forgetSessions(t, a)
	browser.drained(t)
	x.waitClosed(t, opened.Session.Id, "agent stream lost")
	a.jobEnded(t, job)
	a.journaled(t, "terminal session cleanup finished after restart", "session="+opened.Session.Id.String())
	if restarts := string(a.systemctl("show", a.unit, "--property=NRestarts")); restarts != "NRestarts=1\n" {
		t.Fatalf("systemd restarts: %s", restarts)
	}

	// The restarted agent takes sessions as before.
	x.awaitSessions(t, a)
	opened = x.open(t, x.owner, x.app, 1, 201)
	browser = x.attachBrowser(t, x.owner, opened)
	browser.send(t, append([]byte{terminal.Input}, "exit 4\n"...))
	browser.waitExit(t, 4)
	x.waitClosed(t, opened.Session.Id, "shell exited")
	a.recordedSession(t, opened.Session.Id, x.f.owner, `"shell exited"`)
}
