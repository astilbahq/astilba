package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"crypto/x509"
	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// This gate is deliberately explicit: it modifies only a uniquely named unit on a disposable native host.
func TestNativeAgentService(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires an explicitly disposable Ubuntu systemd host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native agent test requires root on disposable Linux")
	}
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	tag := "astilba-p2-" + uuid.NewString()
	root := filepath.Join("/run", tag)
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	unit := tag + ".service"
	unitPath := filepath.Join("/run/systemd/system", unit)
	systemctl := func(args ...string) []byte {
		t.Helper()
		out, e := exec.Command("systemctl", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("systemctl %s: %s %v", args, out, e)
		}
		return out
	}
	defer func() {
		_ = exec.Command("systemctl", "stop", unit).Run()
		_ = os.Remove(unitPath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(public)
	keyPath := filepath.Join(root, "release.pem")
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
	build := func(version, name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+version, "-o", path, "./cmd/agent")
		cmd.Dir = "../.."
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("agent build: %s %v", out, e)
		}
		return path
	}
	binary := build("fixture.1", "agent")
	next := build("fixture.2", "next-agent")
	data, e := os.ReadFile(next)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(data)
	release := api.AgentRelease{Version: "fixture.2", Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(data)), Sha256: hex.EncodeToString(digest[:])}
	release.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, agentrelease.Message(release)))
	if e = os.WriteFile(filepath.Join(root, release.Sha256), data, 0755); e != nil {
		t.Fatal(e)
	}
	f.execution.Releases = []api.AgentRelease{release}
	f.apiServer.ArtifactDirectory = root
	tlsServer := httptest.NewTLSServer(f.server.Config.Handler)
	defer tlsServer.Close()
	caPath := filepath.Join(root, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600)
	var enrollment api.Enrollment
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers", api.CreateServer{Name: "Native host", Role: "deployment"}, 201, &enrollment)
	state := filepath.Join(root, "state")
	cmd := exec.Command(binary, "enroll", "--state", state, "--management", tlsServer.URL, "--server", enrollment.Server.Id.String(), "--release-key", keyPath, "--docker", os.Getenv("PLAT_APP_DOCKER_HOST"))
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+caPath)
	cmd.Stdin = strings.NewReader(enrollment.Token)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("native enrollment: %s %v", out, e)
	}
	template, e := os.ReadFile("../../host/astilba-agent.service")
	if e != nil {
		t.Fatal(e)
	}
	text := strings.ReplaceAll(string(template), "ExecStart=/usr/local/bin/astilba-agent run", "ExecStart="+binary+" run --state "+state+" --docker "+os.Getenv("PLAT_APP_DOCKER_HOST")+" --executable "+binary)
	text = strings.ReplaceAll(text, "ReadWritePaths=/var/lib/astilba-agent /usr/local/bin", "ReadWritePaths="+root)
	text = strings.Replace(text, "[Service]", "[Service]\nEnvironment=SSL_CERT_FILE="+caPath, 1)
	if e = os.WriteFile(unitPath, []byte(text), 0644); e != nil {
		t.Fatal(e)
	}
	if out, e := exec.Command("systemd-analyze", "verify", unitPath).CombinedOutput(); e != nil {
		t.Fatalf("unit verification: %s %v", out, e)
	}
	systemctl("daemon-reload")
	systemctl("start", unit)
	base := "/api/v1/workspaces/" + ws.String()
	apply := func(action api.TestAction) api.Operation {
		t.Helper()
		var plan api.ExecutionPlan
		b.call("POST", base+"/plans", api.PlanRequest{ServerId: enrollment.Server.Id, EnvironmentId: env, Action: action}, 201, &plan)
		var op api.Operation
		b.call("POST", base+"/plans/"+plan.Id.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
		return op
	}
	await := func(id uuid.UUID) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			var op api.Operation
			var all []api.Operation
			b.call("GET", base+"/operations", nil, 200, &all)
			for _, o := range all {
				if o.Id == id {
					op = o
				}
			}
			if op.State == "succeeded" {
				return
			}
			if op.State == "failed" || op.State == "needs_attention" {
				t.Fatalf("native operation: %+v", op)
			}
			time.Sleep(200 * time.Millisecond)
		}
		logs, _ := exec.Command("journalctl", "-u", unit, "--no-pager", "-n", "20").CombinedOutput()
		t.Fatalf("native operation timed out: %s", logs)
	}
	op := apply("start")
	await(op.Id)
	systemctl("stop", unit)
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	defer docker.Close()
	probe := api.ExecutionCommand{ResourceId: uuid.NewSHA1(enrollment.Server.Id, []byte(env.String()+":probe:v1"))}
	inspect, e := docker.ContainerInspect(context.Background(), agent.Name(probe), client.ContainerInspectOptions{})
	if e != nil || !inspect.Container.State.Running {
		t.Fatal("stopping agent stopped workload", e)
	}
	defer func() {
		v, e := docker.ContainerInspect(context.Background(), agent.Name(probe), client.ContainerInspectOptions{})
		if e == nil && v.Container.Config.Labels["com.astilba.server"] == enrollment.Server.Id.String() {
			_, _ = docker.ContainerRemove(context.Background(), v.Container.ID, client.ContainerRemoveOptions{Force: true})
		}
	}()
	systemctl("start", unit)
	// Signed dashboard update must be deferred by admission while a workload mutation is active.
	var update api.Operation
	b.call("POST", base+"/servers/"+enrollment.Server.Id.String()+"/update", api.UpdateAgent{Version: "fixture.2"}, 201, &update)
	await(update.Id)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var servers []api.Server
		b.call("GET", base+"/servers", nil, 200, &servers)
		if len(servers) > 0 && servers[0].Capabilities != nil && servers[0].Capabilities.Version == "fixture.2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement process did not report new version")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if old, e := os.ReadFile(binary + ".previous"); e != nil || len(old) == 0 {
		t.Fatal("previous executable missing")
	}
	await(apply("remove").Id)
}
