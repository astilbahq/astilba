package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// This test installs the production runtime and canonical agent paths, so it
// requires a separately prepared, explicitly owned disposable Ubuntu host.
func TestBootstrapOnDisposableHost(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_BOOTSTRAP") != "1" {
		t.Skip("requires a fresh owned Ubuntu host")
	}
	owner := os.Getenv("ASTILBA_BOOTSTRAP_OWNER")
	marker, e := os.ReadFile("/etc/astilba-bootstrap-test-owner")
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || owner == "" || e != nil || strings.TrimSpace(string(marker)) != owner {
		t.Fatal("disposable host ownership not established")
	}
	for _, p := range []string{"/usr/local/bin/astilba-agent", "/var/lib/astilba-agent", "/etc/astilba/release-public.pem", "/etc/systemd/system/astilba-agent.service", "/var/lib/docker", "/var/lib/containerd"} {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("host is not fresh: %s", p)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "bootstrap.sh")
	if e = os.WriteFile(script, Bootstrap(), 0600); e != nil {
		t.Fatal(e)
	}
	run := func(env []string, args ...string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		return cmd.CombinedOutput()
	}
	status, e := os.ReadFile("/var/lib/dpkg/status")
	if e != nil {
		t.Fatal(e)
	}
	if out, e := run(nil, "--check"); e != nil {
		t.Fatalf("check: %s %v", out, e)
	}
	after, _ := os.ReadFile("/var/lib/dpkg/status")
	if !bytes.Equal(status, after) {
		t.Fatal("check mode changed installed packages")
	}
	binary := filepath.Join(dir, "agent")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version=onboarding-fixture", "-o", binary, "./cmd/agent")
	build.Dir = ".."
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %s %v", out, e)
	}
	data, e := os.ReadFile(binary)
	if e != nil {
		t.Fatal(e)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(public)
	key := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	digest := sha256.Sum256(data)
	release := api.AgentRelease{Version: "onboarding-fixture", Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Sha256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
	signed := func(v api.AgentRelease) api.AgentRelease {
		v.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, agentrelease.Message(v)))
		return v
	}
	release = signed(release)
	var mu sync.Mutex
	current := release
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v := current
		mu.Unlock()
		switch {
		case r.URL.Path == "/agent-public-key.pem":
			_, _ = w.Write(key)
		case strings.HasSuffix(r.URL.Path, "/message"):
			_, _ = w.Write(agentrelease.Message(v))
		case strings.HasSuffix(r.URL.Path, "/signature"):
			sig, _ := base64.StdEncoding.DecodeString(v.Signature)
			_, _ = w.Write(sig)
		default:
			_, _ = w.Write(data)
		}
	}))
	defer server.Close()
	roots, e := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	if e != nil {
		t.Fatal(e)
	}
	ca := filepath.Join(dir, "roots.pem")
	roots = append(roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})...)
	if e = os.WriteFile(ca, roots, 0600); e != nil {
		t.Fatal(e)
	}
	args := func(v api.AgentRelease) []string {
		return []string{"--apply", "--management", server.URL, "--release", v.Sha256, "--key-fingerprint", agentrelease.Fingerprint(public), "--role", "combined", "--certificate-contact", "ops+fixture@example.test", "--install-runtime", "--install-backups"}
	}
	bad := release
	bad.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	wrongArch := release
	if runtime.GOARCH == "amd64" {
		wrongArch.Arch = "arm64"
	} else {
		wrongArch.Arch = "amd64"
	}
	wrongArch = signed(wrongArch)
	wrongProtocol := release
	wrongProtocol.Protocol = 2
	wrongProtocol = signed(wrongProtocol)
	wrongHash := release
	wrongHash.Sha256 = strings.Repeat("b", 64)
	wrongHash = signed(wrongHash)
	for _, v := range []api.AgentRelease{bad, wrongArch, wrongProtocol, wrongHash} {
		mu.Lock()
		current = v
		mu.Unlock()
		if out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, args(v)...); e == nil {
			t.Fatalf("untrusted release accepted: %s", out)
		} else if bytes.Contains(out, []byte("Management must")) || bytes.Contains(out, []byte("curl:")) {
			t.Fatalf("release verification was not reached: %s", out)
		}
		if _, e = os.Lstat("/usr/local/bin/astilba-agent"); !os.IsNotExist(e) {
			t.Fatal("untrusted release installed a binary")
		}
		if _, e = os.Lstat("/var/lib/docker"); !os.IsNotExist(e) {
			t.Fatal("untrusted release installed runtime")
		}
	}
	mu.Lock()
	current = release
	mu.Unlock()
	if out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, args(release)...); e != nil {
		t.Fatalf("fresh production bootstrap: %s %v", out, e)
	}
	// Cleanup is limited to paths just created by this fixture, after checking the
	// installed binary/key and journal ownership; the runtime stays for host checks.
	cleanAgent := func() bool {
		installed, e := os.ReadFile("/usr/local/bin/astilba-agent")
		storedKey, ke := os.ReadFile("/etc/astilba/release-public.pem")
		if e != nil || ke != nil || !bytes.Equal(installed, data) || !bytes.Equal(storedKey, key) {
			t.Error("refusing cleanup: installed agent identity changed")
			return false
		}
		entries, e := os.ReadDir("/var/lib/astilba-agent")
		if e != nil {
			t.Error(e)
			return false
		}
		for _, entry := range entries {
			if entry.Name() != "fixture-owner" {
				t.Error("refusing cleanup: unexpected journal data")
				return false
			}
		}
		_, _ = exec.Command("systemctl", "disable", "--now", "astilba-agent").CombinedOutput()
		for _, p := range []string{"/usr/local/bin/astilba-agent", "/etc/astilba/release-public.pem", "/etc/systemd/system/astilba-agent.service", "/etc/systemd/system/astilba-agent.service.d", "/var/lib/astilba-agent"} {
			if e := os.RemoveAll(p); e != nil {
				t.Error(e)
			}
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
		return true
	}
	defer func() {
		if _, err := os.Lstat("/usr/local/bin/astilba-agent"); err == nil {
			cleanAgent()
		}
	}()

	canary := filepath.Join("/var/lib/astilba-agent", "fixture-owner")
	if e = os.WriteFile(canary, []byte(owner), 0600); e != nil {
		t.Fatal(e)
	}
	if out, e := run(nil, "--check"); e == nil {
		t.Fatalf("existing agent accepted: %s", out)
	}
	retained, _ := os.ReadFile(canary)
	if string(retained) != owner {
		t.Fatal("existing journal changed")
	}
	if out, e := exec.Command("systemctl", "is-active", "astilba-agent").CombinedOutput(); e == nil || strings.TrimSpace(string(out)) == "active" {
		t.Fatal("bootstrap started the agent without enrollment")
	}
	command := exec.Command("systemctl", "show", "astilba-agent", "--property=ExecStart")
	out, e := command.Output()
	if e != nil || !bytes.Contains(out, []byte("--acme-email ops+fixture@example.test")) {
		t.Fatalf("certificate contact missing from service: %s %v", out, e)
	}
	if !cleanAgent() {
		t.Fatal("cannot prepare second fixture installation")
	}
	// A stopped official daemon with its socket armed must remain stopped in
	// check mode. Inspect without triggering activation, then restore explicitly.
	if out, e := exec.Command("systemctl", "stop", "docker.service").CombinedOutput(); e != nil {
		t.Fatalf("stop Docker: %s %v", out, e)
	}
	if e := exec.Command("systemctl", "is-active", "--quiet", "docker.socket").Run(); e != nil {
		t.Fatal("socket-activation fixture is not armed")
	}
	checkOut, checkErr := run(nil, "--check")
	activated := exec.Command("systemctl", "is-active", "--quiet", "docker.service").Run() == nil
	if out, e := exec.Command("systemctl", "start", "docker.service").CombinedOutput(); e != nil {
		t.Fatalf("restore Docker: %s %v", out, e)
	}
	if checkErr == nil || activated || !bytes.Contains(checkOut, []byte("socket activation")) {
		t.Fatalf("check changed stopped runtime: %s activated=%v err=%v", checkOut, activated, checkErr)
	}
	// The optional-tool flag must not hide a compatible normal-PATH executable.
	resticPath := "/usr/local/bin/restic"
	if _, e = os.Lstat(resticPath); !os.IsNotExist(e) {
		t.Fatal("refusing to overwrite existing normal-PATH backup tool")
	}
	resticData, e := os.ReadFile("/opt/astilba-platform-tools/restic")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(resticPath, resticData, 0755); e != nil {
		t.Fatal(e)
	}
	defer func() {
		retained, e := os.ReadFile(resticPath)
		if e == nil && bytes.Equal(retained, resticData) {
			_ = os.Remove(resticPath)
		} else {
			t.Error("refusing cleanup of changed backup tool")
		}
	}()
	withoutBackups := args(release)
	withoutBackups = withoutBackups[:len(withoutBackups)-1]
	out, e = run([]string{"CURL_CA_BUNDLE=" + ca}, withoutBackups...)
	if e != nil || !bytes.Contains(out, []byte(`"backups":true`)) || !bytes.Contains(out, []byte(`"upgrades":true`)) {
		t.Fatalf("normal-PATH backup capability omitted: %s %v", out, e)
	}

}
