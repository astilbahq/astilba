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
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		return runBootstrap(t, env, append([]string{script}, args...)...)
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
	// Half the CI lanes install from this host's own package sources; the others block them.
	if os.Getenv("ASTILBA_BOOTSTRAP_BLOCK_UBUNTU_ARCHIVES") == "1" {
		sources := aptSources(t)
		blocked, restore := blockUbuntuArchives(t)
		out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, args(release)...)
		restore()
		if e != nil {
			t.Fatalf("fresh production bootstrap with this host's Ubuntu archives blocked (%s): %s %v", strings.Join(blocked, ", "), out, e)
		}
		used := regexp.MustCompile(`Ubuntu packages for this run come from (\S+)\. This host's package sources are unchanged\.`).FindSubmatch(out)
		if !bytes.Contains(out, []byte("This host's package sources failed.")) || used == nil {
			t.Fatalf("the bootstrap did not say it used a fallback archive: %s", out)
		}
		if u, err := url.Parse(string(used[1])); err != nil || slices.Contains(blocked, u.Hostname()) {
			t.Fatalf("installed from a blocked archive: %s", used[1])
		}
		if after := aptSources(t); !maps.Equal(sources, after) {
			t.Fatal("the fallback changed this host's package sources")
		}
	} else if out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, args(release)...); e != nil {
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
	// The runtime this bootstrap installs must meet the terminal session runc floor.
	preflight, e := exec.Command(binary, "preflight", "--state", dir, "--docker", "unix:///var/run/docker.sock").Output()
	if e != nil {
		t.Fatalf("preflight: %v", e)
	}
	var heartbeat api.AgentHeartbeat
	if e = json.Unmarshal(preflight, &heartbeat); e != nil || heartbeat.Capabilities.Troubleshooting == nil || !*heartbeat.Capabilities.Troubleshooting {
		t.Fatalf("bootstrap runtime cannot offer terminal sessions: %s %v", preflight, e)
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

// aptSources reads this host's apt source files.
func aptSources(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("/etc/apt/sources.list.d/*")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, file := range append(files, "/etc/apt/sources.list", "/etc/apt/apt-mirrors.txt") {
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		sources[file] = string(data)
	}
	return sources
}

// blockUbuntuArchives makes the Ubuntu archives in this host's apt sources, and Canonical's own,
// refuse connections, as when the distribution's mirror names stopped resolving on 11 November
// 2026. It returns the names it blocked, and a function that restores /etc/hosts, which also
// runs when the test ends.
func blockUbuntuArchives(t *testing.T) ([]string, func()) {
	t.Helper()
	archive := regexp.MustCompile(`(?m)\bhttps?://([A-Za-z0-9.-]+)(:[0-9]+)?/[^\s\]]*\bubuntu(-ports)?/?(\s|$)`)
	var fromSources []string
	for _, data := range aptSources(t) {
		for _, match := range archive.FindAllStringSubmatch(data, -1) {
			fromSources = append(fromSources, strings.ToLower(match[1]))
		}
	}
	if len(fromSources) == 0 {
		t.Fatal("found no Ubuntu archive in this host's apt sources to block")
	}
	names := append(fromSources, "archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com")
	slices.Sort(names)
	names = slices.Compact(names)
	original, err := os.ReadFile("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	blocked := bytes.TrimRight(bytes.Clone(original), "\n")
	blocked = append(blocked, "\n# Ubuntu archives blocked by TestBootstrapOnDisposableHost\n"...)
	for _, name := range names {
		blocked = fmt.Appendf(blocked, "127.0.0.1 %s\n::1 %s\n", name, name)
	}
	if err = os.WriteFile("/etc/hosts", blocked, 0o644); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			if err := os.WriteFile("/etc/hosts", original, 0o644); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(restore)
	return names, restore
}

const (
	bootstrapLimit = 8 * time.Minute
	// How long output may stay open after the script ends, or its group is killed.
	bootstrapDrainLimit = 15 * time.Second
)

// runBootstrap runs the script in its own process group and streams its output to the job
// log, so a timeout still shows how far it got. A customer's terminal or log pipe waits for
// whatever holds the script's output, so anything still running once it has ended fails the
// test, and is named. The pipe is created here for that: only its inode identifies a holder
// that left the group.
func runBootstrap(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	info, err := write.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pipe := fmt.Sprintf("pipe:[%d]", info.Sys().(*syscall.Stat_t).Ino)
	ctx, cancel := context.WithTimeout(context.Background(), bootstrapLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = write, write
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		t.Logf("bootstrap passed its %s limit. Processes:\n%s", bootstrapLimit, processTable())
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	started := time.Now()
	err = cmd.Start()
	write.Close()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.MultiWriter(&output, os.Stdout), read)
	}()
	err = cmd.Wait()
	ended := time.Since(started).Round(time.Second)
	select {
	case <-drained:
	case <-time.After(bootstrapDrainLimit):
	}
	left := bootstrapLeftovers(cmd.Process.Pid, pipe)
	table := ""
	if len(left) > 0 {
		table = processTable()
	}
	// Normally nothing is left to receive this. It ends the group's own stragglers.
	if e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); e != nil && !errors.Is(e, syscall.ESRCH) {
		t.Error(e)
	}
	read.Close()
	<-drained
	if len(left) > 0 {
		t.Fatalf("bootstrap ended after %s (%v) and left running:\n%s\nProcesses:\n%s", ended, err, strings.Join(left, "\n"), table)
	}
	if ctx.Err() != nil {
		t.Fatalf("bootstrap passed its %s limit: %v", bootstrapLimit, err)
	}
	return output.Bytes(), err
}

// bootstrapLeftovers lists processes in the script's group or holding its output.
func bootstrapLeftovers(group int, pipe string) []string {
	var left []string
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		// The process name in stat can hold spaces, and is followed by: state ppid pgrp.
		_, after, _ := bytes.Cut(stat, []byte(") "))
		fields := strings.Fields(string(after))
		held := false
		fds, _ := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
			held = held || target == pipe
		}
		if !held && (len(fields) < 3 || fields[2] != strconv.Itoa(group)) {
			continue
		}
		command, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		left = append(left, fmt.Sprintf("pid %d stat=%s holds-output=%v: %s", pid, strings.Join(fields[:min(3, len(fields))], ","), held, bytes.ReplaceAll(command, []byte{0}, []byte{' '})))
	}
	return left
}

func processTable() string {
	out, err := exec.Command("ps", "-eo", "pid,ppid,pgid,sid,etimes,stat,wchan:24,args", "--forest").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("ps: %v", err)
	}
	return string(out)
}
