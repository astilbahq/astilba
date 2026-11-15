package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	root := nativeRoot(t, "astilba-p2")
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(public)
	keyPath := filepath.Join(root, "release.pem")
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
	binary, next := filepath.Join(root, "agent"), filepath.Join(root, "next-agent")
	buildNativeAgent(t, binary, "0.1.0-fixture.1")
	buildNativeAgent(t, next, "0.1.0-fixture.2")
	data, e := os.ReadFile(next)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(data)
	release := api.AgentRelease{Version: "0.1.0-fixture.2", MinimumVersion: agentFixtureFloor, Commit: agentFixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(data)), Sha256: hex.EncodeToString(digest[:])}
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
	unit, systemctl := installAgentUnit(t, root, binary, state, caPath)
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
	// Enrollment sealed the journal's keyring to this machine, and the service, in its sandbox,
	// opened it.
	var keyring struct {
		Version int            `json:"agentStateVersion"`
		Keyring jsontext.Value `json:"keyring"`
	}
	if raw, err := os.ReadFile(filepath.Join(state, "keyring.json")); err != nil || json.Unmarshal(raw, &keyring) != nil || keyring.Version != agent.StateVersion || keyring.Keyring != nil {
		t.Fatalf("the service's journal keyring isn't sealed: %v %+v", err, keyring)
	}
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
	b.call("POST", base+"/servers/"+enrollment.Server.Id.String()+"/update", api.UpdateAgent{Version: "0.1.0-fixture.2"}, 201, &update)
	await(update.Id)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var servers []api.Server
		b.call("GET", base+"/servers", nil, 200, &servers)
		if len(servers) > 0 && servers[0].Capabilities != nil && servers[0].Capabilities.Version == "0.1.0-fixture.2" {
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
