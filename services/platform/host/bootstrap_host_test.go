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
	"sync/atomic"
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
		return runBootstrap(t, env, nil, append([]string{script}, args...)...)
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
	const version, minimum, commit = "0.1.0-onboarding-fixture", "0.0.0", "0123456789abcdef0123456789abcdef01234567"
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+version+" -X main.minimumVersion="+minimum+" -X main.commit="+commit, "-o", binary, "./cmd/agent")
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
	// The served file puts another key first, in a block OpenSSL skips. The bootstrap must check
	// and install the key whose fingerprint it was given, and nothing that a reader taking the
	// first block would see.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	otherDER, _ := x509.MarshalPKIXPublicKey(other)
	served := append(pem.EncodeToMemory(&pem.Block{Type: "OTHER KEY", Bytes: otherDER}), key...)
	digest := sha256.Sum256(data)
	release := api.AgentRelease{Version: version, MinimumVersion: minimum, Commit: commit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Sha256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
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
			_, _ = w.Write(served)
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
		return []string{"--apply", "--management", server.URL, "--release", v.Sha256, "--key-fingerprint", agentrelease.Fingerprint(public), "--minimum-version", minimum, "--role", "combined", "--certificate-contact", "ops+fixture@example.test", "--install-runtime", "--install-backups"}
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
	// Correctly signed, but not what the binary was built from.
	wrongCommit := release
	wrongCommit.Commit = strings.Repeat("f", 40)
	wrongCommit = signed(wrongCommit)
	// Correctly signed, but with a minimum version later than its version, which agents refuse.
	inverted := release
	inverted.MinimumVersion = "1.0.0"
	inverted = signed(inverted)
	for _, v := range []api.AgentRelease{bad, wrongArch, wrongProtocol, wrongHash, wrongCommit, inverted} {
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
	// The operator's minimum version, from the same trusted channel as the fingerprint, refuses a
	// correctly signed release that would let the agent go back below it, before installing anything.
	mu.Lock()
	current = release
	mu.Unlock()
	floored := args(release)
	floored[slices.Index(floored, "--minimum-version")+1] = "0.2.0"
	if out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, floored...); e == nil || !bytes.Contains(out, []byte("below 0.2.0, the minimum version you gave")) {
		t.Fatalf("a release below the operator's minimum version was not refused: %s %v", out, e)
	}
	for _, p := range []string{"/usr/local/bin/astilba-agent", "/var/lib/docker"} {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("a release below the operator's minimum version installed %s", p)
		}
	}
	// The CI lanes divide what they check. "blocked" blocks this host's Ubuntu archives, so the
	// bootstrap must install from a fallback archive; "host" installs from this host's own
	// sources; anything else leaves the runner as it is, which needs nothing downloaded.
	ubuntuSources := os.Getenv("ASTILBA_BOOTSTRAP_UBUNTU_SOURCES")
	archives := ubuntuArchives(t)
	if canonical := func(name string) bool { return strings.HasSuffix(name, ".ubuntu.com") }; ubuntuSources == "blocked" && !slices.ContainsFunc(archives, canonical) {
		// CI's own archive step has already moved this runner to a mirror, during an outage of
		// Canonical's archive, and blocking that mirror too would leave too few to fall back to.
		fmt.Println("::warning::Canonical's Ubuntu archive is unavailable, so CI moved this runner to a mirror. This lane checked the bootstrap's install from the host's own sources, not its fallback.")
		ubuntuSources = "host"
	}
	if ubuntuSources == "blocked" || ubuntuSources == "host" {
		// A runner already has every other Ubuntu package the bootstrap needs. Without nftables
		// it must download one. Ubuntu's server images depend on it through ubuntu-standard,
		// which goes too.
		remove := exec.Command("apt-get", "remove", "--yes", "nftables")
		remove.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, e := remove.CombinedOutput(); e != nil {
			t.Fatalf("remove nftables: %s %v", out, e)
		}
	}
	switch ubuntuSources {
	case "blocked":
		sources := aptSources(t)
		blocked, restore := blockHosts(t, append(archives, "archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com"))
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
	case "host":
		// A fresh VM's unattended-upgrades often holds dpkg's lock for its first minutes. The
		// bootstrap must wait for it rather than fail. Ubuntu's apt waits without a word, so the
		// lock is held until the bootstrap announces an install and for 15 seconds after: an
		// install that didn't wait would fail in that time.
		unlock := holdDpkgLock(t)
		var seen bytes.Buffer
		var installing sync.Once
		var released atomic.Bool
		watch := func(chunk []byte) {
			seen.Write(chunk)
			if bytes.Contains(seen.Bytes(), []byte("waits up to 5 minutes for it to finish")) {
				installing.Do(func() {
					time.AfterFunc(15*time.Second, func() { released.Store(true); unlock() })
				})
			}
		}
		out, e := runBootstrap(t, []string{"CURL_CA_BUNDLE=" + ca}, watch, append([]string{script}, args(release)...)...)
		if e != nil {
			t.Fatalf("fresh production bootstrap while dpkg's lock was held: %s %v", out, e)
		}
		if !released.Load() {
			t.Fatalf("the bootstrap finished without installing while dpkg's lock was held: %s", out)
		}
		if !bytes.Contains(out, []byte("Refreshing this host's package sources.")) {
			t.Fatalf("the bootstrap did not refresh this host's sources: %s", out)
		}
		// The runner's sources were just refreshed, so judging them not current would be a fault in
		// the check itself.
		if bytes.Contains(out, []byte("indexes are not current")) {
			t.Fatalf("the bootstrap judged this runner's just-refreshed indexes not current: %s", out)
		}
		// A brief failure of one of the runner's sources is no fault of the bootstrap, which then
		// rightly falls back, but it means this lane didn't check the host's sources this time.
		if bytes.Contains(out, []byte("This host's package sources failed.")) {
			fmt.Println("::warning::The bootstrap fell back from this runner's package sources, so this lane didn't check an install from them; its output above says why.")
		}
	default:
		if out, e := run([]string{"CURL_CA_BUNDLE=" + ca}, args(release)...); e != nil {
			t.Fatalf("fresh production bootstrap: %s %v", out, e)
		}
	}
	if ubuntuSources == "blocked" || ubuntuSources == "host" {
		if out, e := exec.Command("dpkg-query", "-W", "-f=${db:Status-Status} ${Version}", "nftables").Output(); e != nil || string(out) != "installed 1.0.9-1ubuntu0.1" {
			t.Fatalf("the pinned nftables: %q %v", out, e)
		}
	}
	if pinned, e := os.ReadFile("/etc/astilba/release-public.pem"); e != nil || !bytes.Equal(pinned, key) {
		t.Fatalf("the bootstrap pinned %q, not the verified key alone: %v", pinned, e)
	} else if k, e := agentrelease.PublicKey(pinned); e != nil || !k.Equal(public) {
		t.Fatalf("the agent reads another key from the pinned file: %v", e)
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
	// Without backups, and without the optional minimum version, as the dashboard prepares it
	// when the operator gives none.
	withoutBackups := args(release)
	withoutBackups = withoutBackups[:len(withoutBackups)-1]
	at := slices.Index(withoutBackups, "--minimum-version")
	withoutBackups = slices.Delete(withoutBackups, at, at+2)
	out, e = run([]string{"CURL_CA_BUNDLE=" + ca}, withoutBackups...)
	if e != nil || !bytes.Contains(out, []byte(`"backups":true`)) || !bytes.Contains(out, []byte(`"upgrades":true`)) {
		t.Fatalf("normal-PATH backup capability omitted: %s %v", out, e)
	}

}

// holdDpkgLock takes dpkg's frontend lock as apt and unattended-upgrades do, with an fcntl lock.
// It returns a function that releases it, which also runs when the test ends.
func holdDpkgLock(t *testing.T) func() {
	t.Helper()
	file, err := os.OpenFile("/var/lib/dpkg/lock-frontend", os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err = syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock); err != nil {
		file.Close()
		t.Fatal("dpkg's lock is already held: ", err)
	}
	var once sync.Once
	release := func() { once.Do(func() { file.Close() }) }
	t.Cleanup(release)
	return release
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

// ubuntuArchives names the hosts of this host's Ubuntu archives: the sources apt reads a main
// component from. That leaves out sources such as Docker's, which the bootstrap downloads from.
func ubuntuArchives(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("apt-get", "indextargets", "--no-release-info", "--format", "$(REPO_URI)", "Identifier: Packages", "Component: main").Output()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, repository := range strings.Fields(string(out)) {
		uris := []string{repository}
		// A mirror list names its mirrors in a file.
		if file, ok := strings.CutPrefix(repository, "mirror+file:"); ok {
			list, err := os.ReadFile(strings.TrimSuffix(file, "/"))
			if err != nil {
				t.Fatal(err)
			}
			uris = nil
			for _, line := range strings.Split(string(list), "\n") {
				if fields := strings.Fields(line); len(fields) > 0 && !strings.HasPrefix(fields[0], "#") {
					uris = append(uris, fields[0])
				}
			}
		}
		for _, uri := range uris {
			u, err := url.Parse(uri)
			if err != nil || u.Hostname() == "" {
				t.Fatalf("an Ubuntu archive this test can't block: %q", uri)
			}
			names = append(names, strings.ToLower(u.Hostname()))
		}
	}
	if len(names) == 0 {
		t.Fatal("found no Ubuntu archive in this host's apt sources")
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// blockHosts makes names refuse connections, as when the distribution's mirror names stopped
// resolving on 11 November 2026. It returns the names it blocked, and a function that restores
// /etc/hosts, which also runs when the test ends.
func blockHosts(t *testing.T, names []string) ([]string, func()) {
	t.Helper()
	names = slices.Compact(slices.Sorted(slices.Values(names)))
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
// that left the group. watch, when given, sees the output as it arrives.
func runBootstrap(t *testing.T, env []string, watch func([]byte), args ...string) ([]byte, error) {
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
		writers := []io.Writer{&output, os.Stdout}
		if watch != nil {
			writers = append(writers, watcher(watch))
		}
		_, _ = io.Copy(io.MultiWriter(writers...), read)
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

// watcher passes the bootstrap's output to a function as it arrives.
type watcher func([]byte)

func (w watcher) Write(p []byte) (int, error) {
	w(p)
	return len(p), nil
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
