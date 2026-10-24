// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const hostOwnerLabel = "com.astilba.plat4.run"
const hostProjectLabel = "com.astilba.plat4.project"
const hostSocket = "unix:///run/astilba-plat4/docker.sock"
const hostTools = "/opt/astilba-plat4"
const hostAppScript = `printf '%s' "$BODY" > /www/index.html; exec httpd -f -p 8080 -h /www`

type runtimeProject struct {
	id, body, secret, net, volume, app, db string
}

type ownedRuntimeImage struct {
	reference, id string
}

type runtimeHarness struct {
	docker              *client.Client
	run, dir, config    string
	images              []ownedRuntimeImage
	projects            map[string]bool
	proxy               string
	httpURL, tcpAddress string
}

func hostCommand(ctx context.Context, config string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, hostTools+"/bin/docker", args...)
	cmd.Env = append([]string{"PATH=" + hostTools + "/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + config, "DOCKER_CONFIG=" + config, "DOCKER_HOST=" + hostSocket}, env...)
	return cmd.CombinedOutput()
}

func (h *runtimeHarness) command(t *testing.T, env []string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := hostCommand(ctx, h.config, env, args...)
	if err != nil {
		t.Fatalf("Docker %s failed: %v: %s", args[0], err, out)
	}
	return out
}

func newRuntimeHarness(t *testing.T) *runtimeHarness {
	t.Helper()
	if os.Getenv("PLAT4_HOST_TEST") != "1" {
		t.Skip("set PLAT4_HOST_TEST=1 on the disposable Ubuntu 24.04 fixture host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("host validation must run as root inside the disposable Linux VM")
	}
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil || !bytes.Contains(osRelease, []byte("ID=ubuntu\n")) || !bytes.Contains(osRelease, []byte("VERSION_ID=\"24.04\"")) {
		t.Fatal("Ubuntu 24.04 is required")
	}
	pid1, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(pid1)) != "systemd" {
		t.Fatal("systemd must be PID 1; a container is not a host compatibility test")
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", "astilba-plat4-docker.service").Run(); err != nil {
		t.Fatal("the dedicated fixture systemd Docker service must be active")
	}
	if os.Getenv("PLAT4_ARCH") != runtime.GOARCH || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Fatal("PLAT4_ARCH must explicitly match the native test binary architecture")
	}
	docker, err := client.New(client.WithHost(hostSocket))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h := &runtimeHarness{docker: docker, dir: dir, config: filepath.Join(dir, "docker-config"), run: token(), projects: map[string]bool{}}
	if err := os.MkdirAll(filepath.Join(h.config, "cli-plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hostTools+"/cli-plugins/docker-compose", filepath.Join(h.config, "cli-plugins/docker-compose")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.cleanup(t); docker.Close() })
	version, err := docker.ServerVersion(context.Background(), client.ServerVersionOptions{})
	if err != nil || version.Version != "29.9.2" || version.Arch != runtime.GOARCH {
		t.Fatal("unexpected Docker version or daemon architecture")
	}
	info := h.command(t, nil, "info", "--format", "{{json .SecurityOptions}}")
	if bytes.Contains(info, []byte("rootless")) {
		t.Fatal("rootless Docker is outside this host target")
	}
	compose := strings.TrimSpace(string(h.command(t, nil, "compose", "version", "--short")))
	if compose != "5.6.1" {
		t.Fatalf("unexpected Compose version %s", compose)
	}
	for _, image := range []string{effectImage, postgresImage, proxyImage} {
		if _, err := docker.ImageInspect(context.Background(), image); err != nil {
			t.Fatal("pull the pinned fixture images before running host validation")
		}
	}
	kernel, _ := exec.Command("uname", "-r").Output()
	t.Logf("Ubuntu 24.04; kernel=%s arch=%s Docker=%s API=%s Compose=%s Go=%s systemd PID1, rootful", strings.TrimSpace(string(kernel)), runtime.GOARCH, version.Version, version.APIVersion, compose, runtime.Version())
	return h
}

func (h *runtimeHarness) labels(project string) map[string]string {
	return map[string]string{hostOwnerLabel: h.run, hostProjectLabel: project}
}

func (h *runtimeHarness) cleanup(t *testing.T) {
	t.Helper()
	if t.Failed() && h.proxy != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		logs, _ := hostCommand(ctx, h.config, nil, "logs", h.proxy)
		t.Logf("proxy diagnostics: %s", logs)
		cancel()
	}
	for _, kind := range []string{"container", "volume", "network"} {
		list := []string{kind, "ls", "--quiet", "--filter", "label=" + hostOwnerLabel + "=" + h.run}
		if kind == "container" {
			list = append(list, "--all")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := hostCommand(ctx, h.config, nil, list...)
		if err != nil {
			t.Errorf("cleanup inventory: %v", err)
			cancel()
			continue
		}
		for _, id := range strings.Fields(string(out)) {
			format := "{{json .Labels}}"
			if kind == "container" {
				format = "{{json .Config.Labels}}"
			}
			data, err := hostCommand(ctx, h.config, nil, kind, "inspect", "--format", format, id)
			var labels map[string]string
			if err != nil || json.Unmarshal(data, &labels) != nil || labels[hostOwnerLabel] != h.run || !h.projects[labels[hostProjectLabel]] {
				t.Error("refusing cleanup without verified fixture ownership")
				continue
			}
			args := []string{kind, "rm"}
			if kind == "container" {
				args = append(args, "--force")
			}
			if _, err := hostCommand(ctx, h.config, nil, append(args, id)...); err != nil {
				t.Errorf("cleanup %s: %v", kind, err)
			}
		}
		cancel()
	}
	// Containers must release images before their final owned references are removed.
	for _, image := range h.images {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		id, err := hostCommand(ctx, h.config, nil, "image", "inspect", "--format", "{{.Id}}", image.reference)
		if err != nil || strings.TrimSpace(string(id)) != image.id {
			t.Error("refusing image cleanup without its recorded identity")
		} else if out, err := hostCommand(ctx, h.config, nil, "image", "rm", image.reference); err != nil {
			t.Errorf("image cleanup: %v: %s", err, out)
		}
		cancel()
	}
}

func (h *runtimeHarness) trackImage(t *testing.T, reference string) {
	t.Helper()
	h.images = append(h.images, ownedRuntimeImage{reference: reference, id: h.inspectID(t, reference)})
}

func (h *runtimeHarness) project(suffix string) runtimeProject {
	id := "p" + h.run + suffix
	h.projects[id] = true
	return runtimeProject{id: id, body: "response-" + id, secret: newCredential(), net: id + "-private", volume: id + "-data", app: id + "-app", db: id + "-db"}
}

func appHealth() map[string]any {
	return map[string]any{"test": []string{"CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080"}, "interval": "1s", "timeout": "1s", "retries": 30}
}

func (h *runtimeHarness) composeSpec(p runtimeProject, exported bool) map[string]any {
	password := p.secret
	if exported {
		password = "${DB_PASSWORD:?Set DB_PASSWORD to the existing database password}"
	}
	services := map[string]any{
		"app": map[string]any{"image": effectImage, "command": []string{"sh", "-c", strings.ReplaceAll(hostAppScript, "$", "$$")}, "environment": map[string]string{"BODY": p.body},
			"labels": h.labels(p.id), "read_only": true, "tmpfs": []string{"/www:rw,noexec,nosuid,size=1048576"}, "cap_drop": []string{"ALL"},
			"healthcheck": appHealth(), "networks": map[string]any{"backend": map[string]any{"aliases": []string{p.app}}}},
		"db": map[string]any{"image": postgresImage, "environment": map[string]string{"POSTGRES_USER": "fixture", "POSTGRES_DB": "fixture", "POSTGRES_PASSWORD": password},
			"labels": h.labels(p.id), "volumes": []string{"data:/var/lib/postgresql"},
			"healthcheck": map[string]any{"test": []string{"CMD", "pg_isready", "-U", "fixture", "-d", "fixture"}, "interval": "1s", "timeout": "1s", "retries": 30},
			"networks":    map[string]any{"backend": map[string]any{"aliases": []string{p.db}}}},
	}
	networks := map[string]any{"backend": map[string]any{"name": p.net, "internal": true, "labels": h.labels(p.id)}}
	volume := map[string]any{"name": p.volume, "labels": h.labels(p.id)}
	if exported {
		volume = map[string]any{"name": p.volume, "external": true}
		networks["front"] = map[string]any{"labels": h.labels(p.id)}
		services["proxy"] = map[string]any{"image": proxyImage, "command": proxyArguments(), "labels": h.labels(p.id), "cap_drop": []string{"ALL"},
			"volumes": []string{"./routes:/etc/traefik/runtime:ro"}, "networks": []string{"front", "backend"},
			"ports": []map[string]any{{"target": 8080, "published": "0", "host_ip": "127.0.0.1"}, {"target": 5432, "published": "0", "host_ip": "127.0.0.1"}}}
	}
	return map[string]any{"services": services, "networks": networks, "volumes": map[string]any{"data": volume}}
}

func proxyArguments() []string {
	return []string{"--providers.file.directory=/etc/traefik/runtime", "--providers.file.watch=true", "--providers.providersThrottleDuration=100ms",
		"--entrypoints.web.address=:8080", "--entrypoints.database.address=:5432", "--global.checkNewVersion=false", "--global.sendAnonymousUsage=false", "--log.level=ERROR"}
}

func hostWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *runtimeHarness) deploySDK(t *testing.T, p runtimeProject) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.docker.NetworkCreate(ctx, p.net, client.NetworkCreateOptions{Internal: true, Labels: h.labels(p.id)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: p.volume, Labels: h.labels(p.id)}); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"app", "db"} {
		cfg := &container.Config{Labels: h.labels(p.id)}
		hostCfg := &container.HostConfig{NetworkMode: container.NetworkMode(p.net)}
		name := p.app
		if service == "app" {
			cfg.Image, cfg.Cmd, cfg.Env = effectImage, []string{"sh", "-c", hostAppScript}, []string{"BODY=" + p.body}
			hostCfg.ReadonlyRootfs, hostCfg.CapDrop, hostCfg.Tmpfs = true, []string{"ALL"}, map[string]string{"/www": "rw,noexec,nosuid,size=1048576"}
		} else {
			name = p.db
			cfg.Image, cfg.Env = postgresImage, []string{"POSTGRES_USER=fixture", "POSTGRES_DB=fixture", "POSTGRES_PASSWORD=" + p.secret}
			hostCfg.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: p.volume, Target: "/var/lib/postgresql"}}
		}
		created, err := h.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hostCfg,
			NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{p.net: {Aliases: []string{service, name}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *runtimeHarness) deployCompose(t *testing.T, p runtimeProject) string {
	t.Helper()
	path := filepath.Join(h.dir, p.id+".json")
	hostWriteJSON(t, path, h.composeSpec(p, false))
	h.command(t, nil, "compose", "--env-file", "/dev/null", "--project-name", p.id, "--file", path, "up", "--detach", "--wait", "--wait-timeout", "45")
	return path
}

func (h *runtimeHarness) startProxy(t *testing.T, dir string, projects ...runtimeProject) {
	t.Helper()
	h.projects["shared"] = true
	ctx := context.Background()
	web, tcp := network.MustParsePort("8080/tcp"), network.MustParsePort("5432/tcp")
	created, err := h.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Image: proxyImage,
		Config: &container.Config{Cmd: proxyArguments(), Labels: h.labels("shared")},
		HostConfig: &container.HostConfig{CapDrop: []string{"ALL"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: filepath.Join(dir, "routes"), Target: "/etc/traefik/runtime", ReadOnly: true}},
			PortBindings: network.PortMap{web: {{HostIP: netip.MustParseAddr("127.0.0.1")}}, tcp: {{HostIP: netip.MustParseAddr("127.0.0.1")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.proxy = created.ID
	for _, p := range projects {
		if _, err := h.docker.NetworkConnect(ctx, p.net, client.NetworkConnectOptions{Container: h.proxy}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.docker.ContainerStart(ctx, h.proxy, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	h.httpURL, h.tcpAddress = h.addresses(t, h.proxy)
}

func (h *runtimeHarness) addresses(t *testing.T, id string) (string, string) {
	t.Helper()
	var ports map[string][]struct{ HostIP, HostPort string }
	if err := json.Unmarshal(h.command(t, nil, "inspect", "--format", "{{json .NetworkSettings.Ports}}", id), &ports); err != nil {
		t.Fatal(err)
	}
	address := func(port string) string {
		bindings := ports[port]
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
			t.Fatal("fixture ingress must have one loopback binding per port")
		}
		return net.JoinHostPort(bindings[0].HostIP, bindings[0].HostPort)
	}
	return "http://" + address("8080/tcp"), address("5432/tcp")
}

func runtimeRoute(p runtimeProject, tcp bool) hostRoute {
	return hostRoute{Project: p.id, BackendProject: p.id, Host: p.id + ".test", Body: p.body, TCP: tcp}
}

func runtimeDB(ctx context.Context, address, password string) (*pgx.Conn, error) {
	u := &url.URL{Scheme: "postgres", User: url.UserPassword("fixture", password), Host: address, Path: "fixture"}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return pgx.Connect(bounded, u.String())
}

func (h *runtimeHarness) publisher(dir string, a runtimeProject) *hostPublisher {
	return &hostPublisher{dir: dir, httpURL: h.httpURL, point: func(string) error { return nil }, checkTCP: func(ctx context.Context, r hostRoute) error {
		if r.Project != a.id {
			return errors.New("TCP destination is not approved")
		}
		c, err := runtimeDB(ctx, h.tcpAddress, a.secret)
		if err != nil {
			return errors.New("approved TCP database unavailable")
		}
		defer c.Close(context.Background())
		return c.Ping(ctx)
	}}
}

func runtimeApply(t *testing.T, p *hostPublisher, id string, route *hostRoute, fault string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.apply(ctx, id, route, fault)
}

func runtimeVerify(t *testing.T, p *hostPublisher) {
	t.Helper()
	r, err := p.approved()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := p.verify(ctx, r); err != nil {
		t.Fatal(err)
	}
}

func (h *runtimeHarness) inspectID(t *testing.T, name string) string {
	t.Helper()
	return strings.TrimSpace(string(h.command(t, nil, "inspect", "--format", "{{.Id}}", name)))
}

func (h *runtimeHarness) assertPrivate(t *testing.T, id string) {
	t.Helper()
	var ports map[string][]any
	if err := json.Unmarshal(h.command(t, nil, "inspect", "--format", "{{json .NetworkSettings.Ports}}", id), &ports); err != nil {
		t.Fatal(err)
	}
	for _, bound := range ports {
		if len(bound) != 0 {
			t.Fatal("application or database has a direct host port")
		}
	}
}

func (h *runtimeHarness) removeManagedA(t *testing.T, a runtimeProject) {
	t.Helper()
	ids := h.command(t, nil, "ps", "--all", "--quiet", "--filter", "label="+hostOwnerLabel+"="+h.run, "--filter", "label="+hostProjectLabel+"="+a.id)
	for _, id := range strings.Fields(string(ids)) {
		var labels map[string]string
		if err := json.Unmarshal(h.command(t, nil, "inspect", "--format", "{{json .Config.Labels}}", id), &labels); err != nil || labels[hostOwnerLabel] != h.run || labels[hostProjectLabel] != a.id {
			t.Fatal("refusing to remove foreign container")
		}
		h.command(t, nil, "rm", "--force", id)
	}
	h.command(t, nil, "network", "disconnect", a.net, h.proxy)
	var labels map[string]string
	if err := json.Unmarshal(h.command(t, nil, "network", "inspect", "--format", "{{json .Labels}}", a.net), &labels); err != nil || labels[hostOwnerLabel] != h.run || labels[hostProjectLabel] != a.id {
		t.Fatal("refusing to remove foreign network")
	}
	h.command(t, nil, "network", "rm", a.net)
}

func TestHostRuntime(t *testing.T) {
	h := newRuntimeHarness(t)
	a, b := h.project("a"), h.project("b")
	h.deploySDK(t, a)
	bFile := h.deployCompose(t, b)
	bApp := strings.TrimSpace(string(h.command(t, nil, "compose", "--project-name", b.id, "--file", bFile, "ps", "--quiet", "app")))
	bDB := strings.TrimSpace(string(h.command(t, nil, "compose", "--project-name", b.id, "--file", bFile, "ps", "--quiet", "db")))
	aAppID, bAppID := h.inspectID(t, a.app), h.inspectID(t, bApp)
	bVolume := string(h.command(t, nil, "volume", "inspect", b.volume))
	// This fixture-created sentinel represents an unmanaged workload. Neither
	// project publication nor export operations may select its resources.
	h.projects["sentinel"] = true
	sentinel, sentinelVolume := "sentinel-"+h.run, "sentinel-data-"+h.run
	h.command(t, nil, "volume", "create", "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=sentinel", sentinelVolume)
	h.command(t, nil, "run", "--detach", "--name", sentinel, "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=sentinel",
		"--volume", sentinelVolume+":/data", effectImage, "sh", "-c", "printf sentinel > /data/proof; exec sleep 600")
	sentinelID := h.inspectID(t, sentinel)
	sentinelState := string(h.command(t, nil, "volume", "inspect", sentinelVolume))
	ra, rb := runtimeRoute(a, true), runtimeRoute(b, false)
	unchanged := func(t *testing.T) {
		t.Helper()
		if h.inspectID(t, bApp) != bAppID || h.inspectID(t, sentinel) != sentinelID ||
			string(h.command(t, nil, "volume", "inspect", b.volume)) != bVolume ||
			string(h.command(t, nil, "volume", "inspect", sentinelVolume)) != sentinelState {
			t.Fatal("unrelated workload or volume changed")
		}
		if string(h.command(t, nil, "exec", sentinel, "cat", "/data/proof")) != "sentinel" {
			t.Fatal("unmanaged volume data changed")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		probe := &hostPublisher{httpURL: h.httpURL}
		if err := probe.http(ctx, rb.Host, b.body); err != nil {
			t.Fatal("unrelated HTTP route changed:", err)
		}
	}
	dir := filepath.Join(h.dir, "publisher")
	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.startProxy(t, dir, a, b)
	p := h.publisher(dir, a)
	if err := runtimeApply(t, p, b.id, &rb, ""); err != nil {
		t.Fatal(err)
	}
	if err := runtimeApply(t, p, a.id, &ra, ""); err != nil {
		t.Fatal(err)
	}
	seed, err := runtimeDB(context.Background(), h.tcpAddress, a.secret)
	if err != nil {
		t.Fatal("TCP database route unavailable")
	}
	_, err = seed.Exec(context.Background(), `CREATE TABLE fixture_data(value text); INSERT INTO fixture_data VALUES('preserved-after-export')`)
	seed.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !t.Run("private_defaults_and_project_isolation", func(t *testing.T) {
		for _, id := range []string{a.app, a.db, bApp, bDB} {
			h.assertPrivate(t, id)
		}
		for _, project := range []runtimeProject{a, b} {
			body := h.command(t, nil, "run", "--rm", "--network", project.net, "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"="+project.id,
				effectImage, "wget", "-q", "-O", "-", "http://app:8080")
			if strings.TrimSpace(string(body)) != project.body {
				t.Fatal("project-local service name resolved to another project")
			}
		}
		ip := strings.TrimSpace(string(h.command(t, nil, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", bApp)))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := hostCommand(ctx, h.config, nil, "run", "--rm", "--network", a.net, "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"="+a.id,
			effectImage, "wget", "-T", "1", "-q", "-O", "-", "http://"+net.JoinHostPort(ip, "8080"))
		if err == nil {
			t.Fatal("project A reached B directly across private networks")
		}
		unchanged(t)
	}) {
		return
	}

	if !t.Run("reject_foreign_references_and_route_claims", func(t *testing.T) {
		before, err := os.ReadFile(filepath.Join(dir, "routes", "runtime.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, which := range []string{"foreign_backend", "duplicate_host", "duplicate_tcp"} {
			candidate, id := ra, a.id
			switch which {
			case "foreign_backend":
				candidate.BackendProject = b.id
			case "duplicate_host":
				candidate.Host = rb.Host
			case "duplicate_tcp":
				candidate, id = rb, b.id
				candidate.TCP = true
			}
			if err := runtimeApply(t, p, id, &candidate, ""); err == nil {
				t.Fatalf("accepted %s", which)
			}
			after, err := os.ReadFile(filepath.Join(dir, "routes", "runtime.yaml"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected input changed the applied file")
			}
		}
		runtimeVerify(t, p)
	}) {
		return
	}

	if !t.Run("publication_and_nontransactional_reload_recovery", func(t *testing.T) {
		approved, _ := os.ReadFile(filepath.Join(dir, "approved.json"))
		p.point = func(stage string) error {
			if stage == "publish" {
				return errors.New("injected publication failure")
			}
			return nil
		}
		if err := runtimeApply(t, p, a.id, &ra, ""); err == nil {
			t.Fatal("publication failure was ignored")
		}
		p.point = func(string) error { return nil }
		for _, fault := range []string{"malformed", "missing_backend", "promote", "approved_written"} {
			partialObserved := false
			if fault == "missing_backend" {
				p.point = func(stage string) error {
					if stage == "activated" && !partialObserved {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						if p.http(ctx, ra.Host, a.body) == nil || p.http(ctx, rb.Host, b.body) != nil {
							t.Fatal("expected the missing backend to break A while B remains reachable")
						}
						partialObserved = true
					}
					return nil
				}
			}
			if fault == "promote" || fault == "approved_written" {
				p.point = func(stage string) error {
					if stage == fault {
						return errors.New("injected snapshot failure")
					}
					return nil
				}
			}
			if err := runtimeApply(t, p, a.id, &ra, fault); err == nil {
				t.Fatalf("accepted invalid candidate %s", fault)
			}
			if fault == "missing_backend" && !partialObserved {
				t.Fatal("semantic rejection did not exercise a partially applied graph")
			}
			p.point = func(string) error { return nil }
			runtimeVerify(t, p)
			current, err := os.ReadFile(filepath.Join(dir, "approved.json"))
			if err != nil || !bytes.Equal(approved, current) {
				t.Fatal("failed candidate replaced the approved registry")
			}
			unchanged(t)
		}
		if h.inspectID(t, a.app) != aAppID {
			t.Fatal("routing recovery replaced the application")
		}
	}) {
		return
	}

	if !t.Run("failed_restore_and_proxy_restart", func(t *testing.T) {
		p.point = func(stage string) error {
			if stage == "restore" {
				return errors.New("injected restore write failure")
			}
			return nil
		}
		if err := runtimeApply(t, p, a.id, &ra, "missing_backend"); !errors.Is(err, errHostNeedsAttention) {
			t.Fatal("failed recovery did not require attention")
		}
		p = h.publisher(dir, a)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := p.recover(ctx); err != nil {
			t.Fatal(err)
		}
		candidate := ra
		candidate.Host = "unapproved-" + a.id + ".test"
		if err := runtimeApply(t, p, a.id, &candidate, "interrupted"); !errors.Is(err, errHostInterrupted) {
			t.Fatal("publisher interruption was not exercised")
		}
		zero := 0
		if _, err := h.docker.ContainerRestart(ctx, h.proxy, client.ContainerRestartOptions{Timeout: &zero}); err != nil {
			t.Fatal(err)
		}
		h.httpURL, h.tcpAddress = h.addresses(t, h.proxy)
		p = h.publisher(dir, a)
		if err := p.recover(ctx); err != nil {
			t.Fatal(err)
		}
		runtimeVerify(t, p)
		unchanged(t)
	}) {
		return
	}

	if !t.Run("overlapping_publishers_do_not_lose_routes", func(t *testing.T) {
		first, second := h.publisher(dir, a), h.publisher(dir, a)
		activated, release, waiting := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var firstOnce, waitOnce, releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		first.point = func(stage string) error {
			fail := false
			if stage == "activated" {
				firstOnce.Do(func() { close(activated); <-release; fail = true })
			}
			if fail {
				return errors.New("injected first-publisher verification failure")
			}
			return nil
		}
		second.point = func(stage string) error {
			if stage == "lock_wait" {
				waitOnce.Do(func() { close(waiting) })
			}
			return nil
		}
		candidateA, candidateB := ra, rb
		candidateA.Host = "pending-" + a.id + ".test"
		candidateB.Host = "new-" + b.id + ".test"
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		one, two := make(chan error, 1), make(chan error, 1)
		go func() { one <- first.apply(ctx, a.id, &candidateA, "") }()
		select {
		case <-activated:
		case <-ctx.Done():
			t.Fatal("first candidate did not activate")
		}
		go func() { two <- second.apply(ctx, b.id, &candidateB, "") }()
		select {
		case <-waiting:
		case <-ctx.Done():
			t.Fatal("second publisher did not wait for the host lock")
		}
		releaseOnce.Do(func() { close(release) })
		if err := <-one; err == nil {
			t.Fatal("first candidate unexpectedly succeeded")
		}
		if err := <-two; err != nil {
			t.Fatal(err)
		}
		rb = candidateB
		runtimeVerify(t, p)
		approved, err := p.approved()
		if err != nil || approved.Projects[a.id].Host != ra.Host || approved.Projects[b.id].Host != rb.Host {
			t.Fatal("a failed writer discarded another project's update")
		}
		unchanged(t)
	}) {
		return
	}

	t.Run("standalone_compose_export", func(t *testing.T) {
		export := filepath.Join(h.dir, "export-source")
		if err := os.MkdirAll(filepath.Join(export, "routes"), 0o700); err != nil {
			t.Fatal(err)
		}
		hostWriteJSON(t, filepath.Join(export, "compose.json"), h.composeSpec(a, true))
		exported := hostRegistry{Revision: "standalone", Projects: map[string]hostRoute{a.id: ra}}
		routes, err := exported.render("")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(export, "routes", "runtime.yaml"), routes, 0o600); err != nil {
			t.Fatal(err)
		}
		hostWriteJSON(t, filepath.Join(export, "manifest.json"), map[string]any{"format": 1, "images": []string{effectImage, postgresImage, proxyImage}, "retained_volume": a.volume, "required_environment": []string{"DB_PASSWORD"}, "data_included": false})
		readme := "Set DB_PASSWORD to the existing database password. The retained external volume must exist; restore its data first when moving to a fresh host. Run docker compose --project-name exported --file compose.json up --detach --wait. Routes are under ./routes; no management connection is required. Down preserves the external data volume.\n"
		if err := os.WriteFile(filepath.Join(export, "README.txt"), []byte(readme), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := filepath.WalkDir(export, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range []string{a.secret, b.secret, h.dir, hostSocket} {
				if bytes.Contains(data, []byte(secret)) {
					return errors.New("export contains a secret or management dependency")
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := runtimeApply(t, p, a.id, nil, ""); err != nil {
			t.Fatal(err)
		}
		h.removeManagedA(t, a)
		unchanged(t)
		if err := h.docker.Close(); err != nil {
			t.Fatal(err)
		}
		// The management SDK is closed. The remaining lifecycle uses only
		// ordinary Docker/Compose commands and PostgreSQL/HTTP clients.
		copyDir := filepath.Join(t.TempDir(), "standalone")
		if err := os.CopyFS(copyDir, os.DirFS(export)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(export); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(copyDir, "compose.json")
		project := "export" + h.run
		env := []string{"DB_PASSWORD=" + a.secret}
		compose := func(args ...string) []byte {
			return h.command(t, env, append([]string{"compose", "--env-file", "/dev/null", "--project-name", project, "--file", file}, args...)...)
		}
		compose("config", "--quiet")
		for cycle := 0; cycle < 2; cycle++ {
			compose("up", "--detach", "--wait", "--wait-timeout", "45")
			proxyID := strings.TrimSpace(string(compose("ps", "--quiet", "proxy")))
			httpURL, tcpAddress := h.addresses(t, proxyID)
			probe := &hostPublisher{httpURL: httpURL}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := hostAwait(ctx, func() error { return probe.http(ctx, ra.Host, a.body) }); err != nil {
				cancel()
				t.Fatal(err)
			}
			var value string
			err = hostAwait(ctx, func() error {
				c, err := runtimeDB(ctx, tcpAddress, a.secret)
				if err != nil {
					return errors.New("exported TCP route unavailable")
				}
				defer c.Close(context.Background())
				return c.QueryRow(ctx, `SELECT value FROM fixture_data`).Scan(&value)
			})
			cancel()
			if err != nil || value != "preserved-after-export" {
				t.Fatal("export did not retain the database contents")
			}
			for _, service := range []string{"app", "db"} {
				h.assertPrivate(t, strings.TrimSpace(string(compose("ps", "--quiet", service))))
			}
			unchanged(t)
			compose("down")
		}
		unchanged(t)
	})
}
