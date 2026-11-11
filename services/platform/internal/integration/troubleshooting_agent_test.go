package integration

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The whole path: a person opens a session in the real relay, the real agent picks up the
// offer from its poll, and a real shell runs in a container on a real daemon.
func TestNativeTroubleshootingEndToEnd(t *testing.T) {
	if os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host: the agent reads root-owned /proc entries")
	}
	x := relayFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()

	plan := deployment.Plan{EnvironmentID: x.env}
	var generation int64
	if err = x.f.st.Pool.QueryRow(ctx, "SELECT d.server_id,d.id,s.controller_generation FROM deployment_units d JOIN servers s ON s.id=d.server_id WHERE d.environment_id=$1 AND d.primary_unit", x.env).Scan(&plan.ServerID, &plan.UnitID, &generation); err != nil {
		t.Fatal(err)
	}
	name := deployment.UnitName(x.app)
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "120"}, Labels: map[string]string{"com.astilba.server": plan.ServerID.String(), "com.astilba.environment": x.env.String(), "com.astilba.unit": plan.UnitID.String(), "com.astilba.resource": x.app.String(), "com.astilba.kind": "visual-v1"}}, HostConfig: &container.HostConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	journal, err := agent.Open(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err = journal.RecordUnitPlan(plan); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: x.f.server.URL, ControllerGeneration: generation, ServerID: plan.ServerID, Credential: strings.SplitN(x.agentToken, ".", 2)[1], Enrolled: true, Role: api.Deployment}); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetTroubleshooting(true); err != nil {
		t.Fatal(err)
	}
	// The host's journal, as the agent writes it.
	trail := &lockedLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(trail, nil)))
	defer slog.SetDefault(previous)
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker})
	host.HTTP = x.f.server.Client()
	yes := true
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true, Troubleshooting: &yes, TroubleshootingEnabled: &yes}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 1 << 30, Notes: []string{}}}
	}
	x.capabilities(t, func(*api.AgentCapabilities) {}) // only this agent's own report counts
	stopped := make(chan error, 1)
	go func() { stopped <- host.Run(ctx) }()
	defer func() {
		cancel()
		<-stopped
	}()

	// An agent reports an extension only after a response shows the controller accepts it.
	reported := false
	for deadline := time.Now().Add(40 * time.Second); !reported && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if err = x.f.st.Pool.QueryRow(ctx, "SELECT COALESCE(capabilities->>'troubleshooting'='true' AND capabilities->>'troubleshootingEnabled'='true',false) FROM servers WHERE id=$1", plan.ServerID).Scan(&reported); err != nil {
			t.Fatal(err)
		}
	}
	opened := x.open(t, x.owner, x.app, 1, 201)
	browser := x.attachBrowser(t, x.owner, opened)
	browser.send(t, terminal.ResizeFrame(80, 24))
	browser.send(t, append([]byte{terminal.Input}, "echo end''to''end; exit 3\n"...))
	output := strings.Builder{}
exited:
	for deadline := time.After(60 * time.Second); ; {
		select {
		case frame, open := <-browser.frames:
			if !open {
				t.Fatalf("stream ended early: %q", output.String())
			}
			if frame[0] == terminal.Output {
				output.Write(frame[1:])
				continue
			}
			if frame[0] != terminal.Exit || len(frame) != 2 || frame[1] != 3 || !strings.Contains(output.String(), "endtoend") {
				t.Fatalf("frame %v after %q", frame, output.String())
			}
			x.waitClosed(t, opened.Session.Id, "shell exited")
			break exited
		case <-deadline:
			t.Fatalf("no exit: %q", output.String())
		}
	}
	journaled(t, trail, opened.Session.Id.String(), `reason="shell exited"`)

	// A session management ends is journaled with management's reason, not as a lost stream.
	opened = x.open(t, x.owner, x.app, 1, 201)
	browser = x.attachBrowser(t, x.owner, opened)
	browser.send(t, append([]byte{terminal.Input}, "echo run''ning\n"...))
	output.Reset()
	for deadline := time.After(60 * time.Second); !strings.Contains(output.String(), "running"); {
		select {
		case frame, open := <-browser.frames:
			if !open || frame[0] != terminal.Output {
				t.Fatalf("frame %v open=%v after %q", frame, open, output.String())
			}
			output.Write(frame[1:])
		case <-deadline:
			t.Fatalf("shell did not run: %q", output.String())
		}
	}
	x.owner.call("POST", x.prefix+"/troubleshooting-sessions/"+opened.Session.Id.String()+"/close", nil, 204, nil)
	x.waitClosed(t, opened.Session.Id, "closed")
	journaled(t, trail, opened.Session.Id.String(), `reason="ended by management: closed"`)
}

// journaled waits for the agent to record a session's end in the host's journal.
func journaled(t *testing.T, trail *lockedLog, session, reason string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for line := range strings.Lines(trail.String()) {
			if strings.Contains(line, `msg="terminal session ended"`) && strings.Contains(line, session) {
				if !strings.Contains(line, reason) {
					t.Fatalf("journaled %q, want %s", line, reason)
				}
				return
			}
		}
	}
	t.Fatalf("no end journaled for %s:\n%s", session, trail.String())
}
