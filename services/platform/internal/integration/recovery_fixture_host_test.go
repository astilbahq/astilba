package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func runRecoveryFixtureHost(t *testing.T, workers context.Context, service *execution.Service, identity execution.Identity, input api.AgentEnrollment, executor *recoveryFixtureHost, host hostnetwork.Host) {
	t.Helper()
	lastHeartbeat := time.Time{}
	for workers.Err() == nil {
		if time.Since(lastHeartbeat) >= 15*time.Second {
			if _, err := service.Heartbeat(workers, identity, api.AgentHeartbeat{Capabilities: input.Capabilities, Preflight: input.Preflight}); err != nil {
				if workers.Err() == nil {
					t.Error(err)
				}
				return
			}
			lastHeartbeat = time.Now()
		}
		poll, done := context.WithTimeout(workers, time.Second)
		c, err := service.Poll(poll, identity)
		pollErr := poll.Err()
		done()
		if err != nil {
			if waitForFixturePollLease(workers, err) || pollErr != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				continue
			}
			t.Error(err)
			return
		}
		if c == nil {
			continue
		}
		report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
		switch c.Kind {
		case "network":
			var command hostnetwork.Command
			if json.Unmarshal(*c.Network, &command) != nil {
				t.Error("invalid network command")
				return
			}
			key, err := host.Identity(command.MembershipID)
			if err != nil {
				t.Error(err)
				return
			}
			receipt := hostnetwork.Receipt{Identity: key, NetworkID: command.NetworkID}
			if command.Manifest != nil {
				receipt.Generation, receipt.Hash = command.Manifest.Generation, hostnetwork.Hash(*command.Manifest)
			}
			raw, _ := json.Marshal(receipt)
			value := json.RawMessage(raw)
			report.Network = &value
		case "visual":
			var err error
			report, err = executor.Execute(workers, *c)
			if err != nil {
				if workers.Err() == nil {
					t.Error(err)
				}
				return
			}
		case "crosshost":
			var command deployment.CrossHostCommand
			if json.Unmarshal(*c.Crosshost, &command) != nil {
				t.Error("invalid host command")
				return
			}
			if command.Phase == "checkpoint" {
				var err error
				report, err = executor.Execute(workers, *c)
				if err != nil || report.State != "succeeded" {
					if workers.Err() == nil {
						t.Error("checkpoint not durable", err, report.Note)
					}
					return
				}
			} else if command.Phase == "resolve" {
				observation := deployment.Observation{ObservedAt: time.Now().UTC(), StateHash: strings.Repeat("a", 64), Resources: []deployment.Resource{}, Images: map[string]string{}}
				for _, app := range command.Unit.Plan.LocalApplications() {
					observation.Images[app.ID.String()] = *app.Source.Image
				}
				receipt := deployment.CrossHostReceipt{ParentID: command.ParentID, GroupHash: command.GroupHash, UnitID: c.ResourceId, Phase: command.Phase, PlanHash: command.Unit.Plan.Hash, Observation: observation}
				raw, _ := json.Marshal(receipt)
				value := json.RawMessage(raw)
				report.Crosshost = &value
			} else {
				time.Sleep(25 * time.Millisecond)
				continue
			}
		default:
			t.Error("unexpected command", c.Kind)
			return
		}
		if err := service.Report(workers, identity, report); err != nil {
			if workers.Err() == nil {
				t.Error(err)
			}
			return
		}
		if err := executor.Acknowledge(c.Id, report.Sequence); err != nil {
			t.Error(err)
			return
		}
	}
}

// A second primary needs a separate Docker host: ingress ownership is local to
// a machine, even though this SQL fixture keeps all controllers in one process.
func recoveryFixtureDocker(t *testing.T, ctx context.Context, source *client.Client, additionalImages ...string) (*client.Client, string) {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("disposable native Linux host required")
	}
	root, err := os.MkdirTemp("/opt", "astilba-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = os.WriteFile(filepath.Join(root, "owner"), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	owned := func() bool {
		raw, err := os.ReadFile(filepath.Join(root, "owner"))
		if err != nil || string(raw) != owner {
			t.Error("fixture ownership changed; cleanup refused", err)
			return false
		}
		return true
	}
	t.Cleanup(func() {
		if owned() {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			}
		}
	})
	namespace := "astilba-recovery-" + strings.ReplaceAll(owner, "-", "")[:8]
	run := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("native fixture %s: %s (%v)", args[0], out, err)
		}
	}
	run("ip", "netns", "add", namespace)
	t.Cleanup(func() {
		if owned() {
			if out, err := exec.Command("ip", "netns", "delete", namespace).CombinedOutput(); err != nil {
				t.Error(string(out), err)
			}
		}
	})
	run("ip", "-n", namespace, "link", "set", "lo", "up")
	socket := "unix://" + filepath.Join(root, "docker.sock")
	daemon := exec.CommandContext(ctx, "nsenter", "--net=/run/netns/"+namespace, "/opt/astilba-plat4/bin/dockerd", "--host="+socket, "--data-root="+filepath.Join(root, "docker"), "--exec-root="+filepath.Join(root, "exec"), "--pidfile="+filepath.Join(root, "docker.pid"), "--group=root", "--exec-opt", "native.cgroupdriver=systemd")
	daemon.Env = append(os.Environ(), "PATH=/opt/astilba-plat4/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	log, err := os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stdout, daemon.Stderr = log, log
	if err = daemon.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	log.Close()
	t.Cleanup(func() {
		if !owned() {
			return
		}
		_ = daemon.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = daemon.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = daemon.Process.Kill()
			<-done
		}
	})
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	until := time.Now().Add(20 * time.Second)
	for {
		if _, err = docker.Info(ctx, client.InfoOptions{}); err == nil {
			break
		}
		if time.Now().After(until) {
			log, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
			t.Fatalf("fixture Docker failed to start: %v\n%s", err, log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, image := range append([]string{execution.TestImage, deployment.ProxyImage}, additionalImages...) {
		archive, err := source.ImageSave(ctx, []string{image})
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := docker.ImageLoad(ctx, archive)
		if err != nil {
			archive.Close()
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, loaded)
		err = errors.Join(readErr, loaded.Close(), archive.Close())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = docker.ImageInspect(ctx, image); err != nil {
			// OCI transfer preserves content but can omit the registry name.
			named, parseErr := reference.ParseNormalizedNamed(image)
			digested, ok := named.(reference.Digested)
			if parseErr != nil || !ok {
				t.Fatal("fixture requires pinned registry images")
			}
			out, aliasErr := exec.CommandContext(ctx, "/opt/astilba-plat4/bin/ctr", "--address", filepath.Join(root, "exec/containerd/containerd.sock"), "--namespace", "moby", "images", "tag", "moby-dangling@"+digested.Digest().String(), named.Name()+"@"+digested.Digest().String()).CombinedOutput()
			if _, err = docker.ImageInspect(ctx, image); aliasErr != nil || err != nil {
				t.Fatalf("fixture image %s: %s (%v, %v)", image, out, aliasErr, err)
			}
		}
	}
	return docker, namespace
}

type recoveryFixtureHost struct {
	mu               sync.Mutex
	engine           agent.Engine
	namespace, state string
}

type recoveryFixtureRequest struct {
	State     string               `json:"state"`
	Socket    string               `json:"socket"`
	CLI       string               `json:"cli"`
	Command   api.ExecutionCommand `json:"command"`
	Envelope  []byte               `json:"envelope,omitempty"`
	Preflight bool                 `json:"preflight,omitempty"`
	CrashAt   string               `json:"crashAt,omitempty"`
}

type recoveryFixtureReply struct {
	Report    api.AgentReport     `json:"report"`
	Heartbeat *api.AgentHeartbeat `json:"heartbeat,omitempty"`
	Error     string              `json:"error,omitempty"`
}

func newRecoveryFixtureHost(service *execution.Service, identity execution.Identity, journal *agent.Journal, docker *client.Client, cli string) *recoveryFixtureHost {
	return &recoveryFixtureHost{engine: agent.Engine{Journal: journal, Docker: docker, DockerCLI: cli, FetchCheckpoint: func(ctx context.Context, d deployment.RecoveryDescriptor) ([]byte, error) {
		return service.RecoveryCheckpoint(ctx, identity, d.ParentID, d.CheckpointID)
	}}}
}

func (h *recoveryFixtureHost) Execute(ctx context.Context, c api.ExecutionCommand) (api.AgentReport, error) {
	return h.executeAt(ctx, c, "")
}

func (h *recoveryFixtureHost) executeAt(ctx context.Context, c api.ExecutionCommand, crashAt string) (api.AgentReport, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.namespace == "" {
		if err := h.engine.Execute(ctx, c); err != nil {
			return api.AgentReport{}, err
		}
		record, err := h.engine.Journal.Record(c.Id)
		return record.Report, err
	}
	request := recoveryFixtureRequest{State: h.state, Socket: h.engine.Docker.DaemonHost(), CLI: h.engine.DockerCLI, Command: c, CrashAt: crashAt}
	if c.Kind == "crosshost" && c.Crosshost != nil {
		var body deployment.CrossHostCommand
		if err := json.Unmarshal(*c.Crosshost, &body); err != nil {
			return api.AgentReport{}, err
		}
		if body.Phase == "checkpoint" {
			var d deployment.RecoveryDescriptor
			if c.RecoveryCheckpoint == nil || json.Unmarshal(*c.RecoveryCheckpoint, &d) != nil {
				return api.AgentReport{}, errors.New("missing checkpoint descriptor")
			}
			var err error
			request.Envelope, err = h.engine.FetchCheckpoint(ctx, d)
			if err != nil {
				return api.AgentReport{}, err
			}
		}
	}
	reply, err := h.runProcess(ctx, request)
	return reply.Report, err
}

func (h *recoveryFixtureHost) Preflight(ctx context.Context) (api.AgentHeartbeat, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.namespace == "" {
		return h.engine.Journal.Preflight(ctx, h.engine.Docker, "fixture", h.state, h.engine.DockerCLI, "/opt/astilba-platform-tools/restic"), nil
	}
	reply, err := h.runProcess(ctx, recoveryFixtureRequest{State: h.state, Socket: h.engine.Docker.DaemonHost(), CLI: h.engine.DockerCLI, Preflight: true})
	if err != nil {
		return api.AgentHeartbeat{}, err
	}
	if reply.Heartbeat == nil {
		return api.AgentHeartbeat{}, errors.New("native preflight reply is absent")
	}
	return *reply.Heartbeat, nil
}

func (h *recoveryFixtureHost) runProcess(ctx context.Context, request recoveryFixtureRequest) (recoveryFixtureReply, error) {
	input, err := json.Marshal(request)
	if err != nil {
		return recoveryFixtureReply{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return recoveryFixtureReply{}, err
	}
	command := exec.CommandContext(ctx, "nsenter", "--net=/run/netns/"+h.namespace, executable, "-test.run=^TestRecoveryFixtureAgentProcess$")
	command.Env = append(os.Environ(), "ASTILBA_P2_HELPER=1", "ASTILBA_RECOVERY_AGENT_PROCESS=1")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
	var output, diagnostics bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = bytes.NewReader(input), &output, &diagnostics
	if err = h.engine.Journal.Close(); err != nil {
		return recoveryFixtureReply{}, err
	}
	runErr := command.Run()
	h.engine.Journal, err = agent.Open(h.state)
	if runErr != nil || err != nil {
		return recoveryFixtureReply{}, fmt.Errorf("native agent process: %w; %s", errors.Join(runErr, err), diagnostics.String())
	}
	var reply recoveryFixtureReply
	// The test runner can append PASS after the helper's single JSON reply.
	if err = json.NewDecoder(&output).Decode(&reply); err != nil {
		return recoveryFixtureReply{}, fmt.Errorf("native agent reply: %w; %s", err, diagnostics.String())
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

func (h *recoveryFixtureHost) Record(id uuid.UUID) (agent.Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine.Journal.Record(id)
}

func (h *recoveryFixtureHost) Acknowledge(id uuid.UUID, sequence int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.engine.Journal.Record(id); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return h.engine.Journal.Acknowledge(id, sequence)
}
func (h *recoveryFixtureHost) RecoveryEvidence() (deployment.RecoveryEvidence, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine.Journal.RecoveryEvidence()
}
func (h *recoveryFixtureHost) ExportRecovery(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine.Journal.ExportRecovery(path)
}
func (h *recoveryFixtureHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.engine.Journal == nil {
		return nil
	}
	return h.engine.Journal.Close()
}

// Run only as a child of the ownership-checked native fixture. Engine's host
// probes must share the Docker daemon's namespace, just as an installed agent does.
func TestRecoveryFixtureAgentProcess(t *testing.T) {
	if os.Getenv("ASTILBA_RECOVERY_AGENT_PROCESS") != "1" {
		t.Skip("native fixture process helper")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("disposable native Linux host required")
	}
	var request recoveryFixtureRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	journal, err := agent.Open(request.State)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(request.Socket))
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	defer docker.Close()
	reply := recoveryFixtureReply{}
	if request.Preflight {
		heartbeat := journal.Preflight(context.Background(), docker, "fixture", request.State, request.CLI, "/opt/astilba-platform-tools/restic")
		reply.Heartbeat = &heartbeat
	} else {
		engine := agent.Engine{Journal: journal, Docker: docker, DockerCLI: request.CLI, ResticCLI: "/opt/astilba-platform-tools/restic", BackupRoots: []string{filepath.Join(request.State, "backups")}, FetchCheckpoint: func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) { return request.Envelope, nil }}
		engine.Point = func(point string) {
			if point != "" && point == request.CrashAt {
				os.Exit(77)
			}
		}
		runErr := engine.Execute(context.Background(), request.Command)
		record, readErr := journal.Record(request.Command.Id)
		reply.Report = record.Report
		if err = errors.Join(runErr, readErr); err != nil {
			reply.Error = err.Error()
		}
	}
	if err = journal.Close(); err != nil {
		reply.Error = err.Error()
	}
	if err = json.NewEncoder(os.Stdout).Encode(reply); err != nil {
		t.Fatal(err)
	}
}
