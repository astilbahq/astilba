package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	nginxImage   = "nginx:1.30.6-alpine3.24@sha256:0e52bd054126cdc040b1276c0af347aeb02a41b9cfe305d6094767ac30e97949"
	caddyImage   = "caddy:2.11.7-alpine@sha256:e70b164da81ef698b3ad19af799ea6086ce6d9e48ed4079eb9fe26f6fbf14ee5"
	traefikImage = "traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953"
	// Longer than a stream ping's interval twice over, and than the 60 seconds that nginx
	// and Traefik use as defaults for their own timeouts.
	proxySilence = 62 * time.Second
	// The proxies' certificates, as hosting documents them.
	proxyCertificates = "/etc/ssl/platform"
)

// hostingProxySnippets reads the configurations people are told to use, so these tests
// cannot pass on settings the page does not give.
func hostingProxySnippets(t *testing.T) map[string][]string {
	t.Helper()
	page, err := os.ReadFile("../../../../docs/hosting.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(page), "### Terminal sessions behind a reverse proxy")
	if !found {
		t.Fatal("hosting page has no reverse proxy section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	snippets := map[string][]string{}
	for _, match := range regexp.MustCompile("(?s)```(\\w+)\n(.*?)```").FindAllStringSubmatch(section, -1) {
		snippets[match[1]] = append(snippets[match[1]], match[2])
	}
	if len(snippets["nginx"]) != 1 || len(snippets["caddyfile"]) != 1 || len(snippets["yaml"]) != 2 {
		t.Fatalf("hosting page snippets: %v", snippets)
	}
	return snippets
}

type proxyDefinition struct {
	image string
	files func(snippets map[string][]string) map[string]string // container path to content
}

var terminalProxies = map[string]proxyDefinition{
	"nginx": {nginxImage, func(s map[string][]string) map[string]string {
		return map[string]string{"/etc/nginx/conf.d/default.conf": s["nginx"][0]}
	}},
	"caddy": {caddyImage, func(s map[string][]string) map[string]string {
		// Certificates come from the fixture, not an ACME issuer, and only one listener is free to bind.
		caddyfile := "{\n\tadmin off\n\tauto_https disable_redirects\n\tdefault_bind 127.0.0.1\n}\n" + strings.Replace(s["caddyfile"][0], "platform.example.com {", "https://platform.example.com:443 {\n\ttls "+proxyCertificates+"/fullchain.pem "+proxyCertificates+"/privkey.pem", 1)
		return map[string]string{"/etc/caddy/Caddyfile": caddyfile}
	}},
	"traefik": {traefikImage, func(s map[string][]string) map[string]string {
		return map[string]string{"/etc/traefik/traefik.yml": s["yaml"][0], "/etc/traefik/dynamic.yml": s["yaml"][1]}
	}},
}

// startTerminalProxy runs a proxy on the host's network, as hosting describes, with its
// documented configuration adjusted only for this host's addresses and a local certificate.
// It returns the public origin and the certificate to trust.
func startTerminalProxy(t *testing.T, docker *client.Client, definition proxyDefinition, upstream string, snippets map[string][]string, change func(string) string) (string, string) {
	t.Helper()
	ctx := context.Background()
	root := nativeRoot(t, "astilba-p17-proxy")
	certificates := filepath.Join(root, "certificates")
	if err := os.Mkdir(certificates, 0700); err != nil {
		t.Fatal(err)
	}
	writeProxyCertificate(t, certificates)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	listener.Close()
	localize := strings.NewReplacer("127.0.0.1:8080", upstream, "platform.example.com:443", "localhost:"+port, "platform.example.com", "localhost", "listen 443 ssl", "listen "+address+" ssl", `":443"`, `"`+address+`"`)
	binds := []string{certificates + ":" + proxyCertificates + ":ro"}
	for path, content := range definition.files(snippets) {
		local := filepath.Join(root, strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-"))
		if err = os.WriteFile(local, []byte(change(localize.Replace(content))), 0600); err != nil {
			t.Fatal(err)
		}
		binds = append(binds, local+":"+path+":ro")
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-p17-proxy-" + uuid.NewString(), Config: &container.Config{Image: definition.image}, HostConfig: &container.HostConfig{NetworkMode: "host", Binds: binds}})
	if err != nil {
		t.Fatal(err)
	}
	logs := func() string {
		out, err := docker.ContainerLogs(context.Background(), created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if err != nil {
			return err.Error()
		}
		defer out.Close()
		text, _ := io.ReadAll(out)
		return string(text)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("proxy logs:\n%s", logs())
		}
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	origin := "https://localhost:" + port
	ca := filepath.Join(certificates, "fullchain.pem")
	probe := terminalProxyClient(t, ca, nil)
	probe.Timeout = 5 * time.Second
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		response, err := probe.Get(origin + "/api/v1/session")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return origin, ca
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy not forwarding: %v\n%s", err, logs())
		}
	}
}

func writeProxyCertificate(t *testing.T, directory string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "fullchain.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
}

func terminalProxyClient(t *testing.T, caPath string, jar http.CookieJar) *http.Client {
	t.Helper()
	data, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		t.Fatal("proxy certificate")
	}
	return &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
}

// proxied moves the fixture to a public origin behind a proxy: every later request of the
// test, the agent's included, passes through it, and the person signs in there.
func (x troubleshootingFixture) proxied(t *testing.T, origin, caPath string) *browser {
	t.Helper()
	x.f.apiServer.Origin = origin
	x.f.server.URL = origin
	jar, _ := cookiejar.New(nil)
	b := &browser{t, x.f, terminalProxyClient(t, caPath, jar), ""}
	b.session()
	b.login("owner@example.test", x.f.password)
	return b
}

// proxyTestSetup prepares what every proxy subtest shares.
func proxyTestSetup(t *testing.T) (string, *client.Client, map[string][]string) {
	t.Helper()
	binary := filepath.Join(nativeRoot(t, "astilba-p17-build"), "agent")
	buildNativeAgent(t, binary, "fixture.1")
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return binary, docker, hostingProxySnippets(t)
}

// A real session through each proxy's documented configuration, with both streams, the
// agent's polls and the person's requests on the proxy's public HTTPS origin.
func TestNativeTroubleshootingProxies(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires a disposable native Linux host: the proxies share its network with the test")
	}
	binary, docker, snippets := proxyTestSetup(t)
	unchanged := func(text string) string { return text }
	start := func(t *testing.T, definition proxyDefinition, change func(string) string) (troubleshootingFixture, *browser, *serviceAgent) {
		t.Helper()
		x := relayFixture(t)
		x.f.apiServer.TerminalIdle = proxySilence
		origin, caPath := startTerminalProxy(t, docker, definition, x.f.server.Listener.Addr().String(), snippets, change)
		b := x.proxied(t, origin, caPath)
		return x, b, x.startServiceAgent(t, b, binary, origin, caPath)
	}
	carries := func(t *testing.T, x troubleshootingFixture, b *browser, a *serviceAgent) {
		t.Helper()
		// A workspace import is the largest request, and nginx's default limit is far below
		// it. Management refuses this one, but the proxy must not answer for it.
		response, err := b.client.Post(x.f.server.URL+"/api/v1/workspaces/import", "application/json", bytes.NewReader(make([]byte, 2<<20)))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusRequestEntityTooLarge {
			t.Fatal("the proxy refused a workspace import")
		}

		// The shell's own end: its last frame and the close handshake pass through.
		opened := x.open(t, b, x.app, 1, 201)
		peer := x.attachBrowser(t, b, opened)
		peer.send(t, terminal.ResizeFrame(100, 30))
		peer.send(t, append([]byte{terminal.Input}, "stty size; echo through''proxy\n"...))
		if output := peer.waitOutput(t, "throughproxy"); !strings.Contains(output, "30 100") {
			t.Fatalf("the resize did not reach the shell: %q", output)
		}
		job := a.startJob(t, peer, 4321)
		peer.send(t, append([]byte{terminal.Input}, "exit 3\n"...))
		peer.waitExit(t, 3)
		x.waitClosed(t, opened.Session.Id, "shell exited")
		a.waitRunning(t, job, false)
		a.recordedSession(t, opened.Session.Id, x.f.owner, `"shell exited"`)

		// Silence, with only the relay's pings on the wire, reaches the idle limit and not
		// a proxy's. The warning before it and the close after it pass through.
		opened = x.open(t, b, x.app, 1, 201)
		peer = x.attachBrowser(t, b, opened)
		job = a.startJob(t, peer, 4322)
		if !peer.closedWithin(t, proxySilence+30*time.Second) {
			t.Fatal("no warning before the idle limit")
		}
		if status := websocket.CloseStatus(peer.err); status != websocket.StatusNormalClosure {
			t.Fatalf("idle stream ended with %v", peer.err)
		}
		x.waitClosed(t, opened.Session.Id, "idle timeout")
		a.waitRunning(t, job, false)
		// The agent frees the container's slot just after the job has gone, and this next
		// session would otherwise be refused for the rare offer that arrives before it does.
		time.Sleep(time.Second)

		// A person who disappears takes the shell and its jobs along.
		opened = x.open(t, b, x.app, 1, 201)
		peer = x.attachBrowser(t, b, opened)
		job = a.startJob(t, peer, 4323)
		peer.conn.CloseNow()
		x.waitClosed(t, opened.Session.Id, "browser stream lost")
		a.waitRunning(t, job, false)
	}
	for name, definition := range terminalProxies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x, b, a := start(t, definition, unchanged)
			carries(t, x, b, a)
		})
	}
	// Hosting's lowest idle limit: the pings have to keep the stream open by themselves.
	t.Run("nginx at the lowest idle limit", func(t *testing.T) {
		t.Parallel()
		x, b, a := start(t, terminalProxies["nginx"], func(text string) string {
			return strings.NewReplacer("proxy_read_timeout 120s", "proxy_read_timeout 35s", "proxy_send_timeout 120s", "proxy_send_timeout 35s").Replace(text)
		})
		carries(t, x, b, a)
	})
	// Without this, a passing silence above could mean nothing was limiting it.
	t.Run("nginx below the idle limit", func(t *testing.T) {
		t.Parallel()
		x, b, a := start(t, terminalProxies["nginx"], func(text string) string {
			return strings.NewReplacer("proxy_read_timeout 120s", "proxy_read_timeout 20s", "proxy_send_timeout 120s", "proxy_send_timeout 20s").Replace(text)
		})
		opened := x.open(t, b, x.app, 1, 201)
		peer := x.attachBrowser(t, b, opened)
		job := a.startJob(t, peer, 4321)
		peer.closedWithin(t, 30*time.Second)
		a.waitRunning(t, job, false)
		x.closeReason(t, opened.Session.Id, 20*time.Second)
	})
	// A proxy that does not forward upgrades ends the session as soon as the agent tries.
	t.Run("nginx without upgrades", func(t *testing.T) {
		t.Parallel()
		x, b, _ := start(t, terminalProxies["nginx"], func(text string) string {
			return strings.NewReplacer("proxy_set_header Upgrade $http_upgrade;", "", "proxy_set_header Connection $connection_upgrade;", `proxy_set_header Connection "";`).Replace(text)
		})
		opened := x.open(t, b, x.app, 1, 201)
		if conn, status := x.dialBrowser(t, b, opened.Session.Id, x.f.server.URL); conn != nil {
			t.Fatalf("browser stream upgraded: %d", status)
		}
		x.waitClosed(t, opened.Session.Id, "agent stream failed")
	})
}

// closeReason waits for a session to close, for a reason the test does not fix in advance.
func (x troubleshootingFixture) closeReason(t *testing.T, session uuid.UUID, limit time.Duration) string {
	t.Helper()
	var state, reason string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT state,COALESCE(close_reason,'') FROM troubleshooting_sessions WHERE id=$1", session).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if state == "closed" {
			return reason
		}
	}
	t.Fatalf("session still %s after %s", state, limit)
	return ""
}

// The real limits, which only time can test. Run on demand: this takes more than 75 minutes.
func TestTroubleshootingProxySoak(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" || os.Getenv("ASTILBA_NATIVE_SOAK") != "1" {
		t.Skip("set ASTILBA_NATIVE_SOAK=1 on a disposable native Linux host")
	}
	binary, docker, snippets := proxyTestSetup(t)
	for name, definition := range terminalProxies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := relayFixture(t)
			x.f.apiServer.TerminalReviewEvery = 5 * time.Second
			origin, caPath := startTerminalProxy(t, docker, definition, x.f.server.Listener.Addr().String(), snippets, func(text string) string { return text })
			b := x.proxied(t, origin, caPath)
			a := x.startServiceAgent(t, b, binary, origin, caPath)

			opened := x.open(t, b, x.app, 1, 201)
			peer := x.attachBrowser(t, b, opened)
			job := a.startJob(t, peer, 4321)
			typed := time.Now()
			peer.closedWithin(t, domain.TroubleshootingIdle+time.Minute)
			silent := time.Since(typed)
			reason := x.closeReason(t, opened.Session.Id, 20*time.Second)
			t.Logf("idle session ended after %s, %v, reason %q", silent, peer.err, reason)
			a.waitRunning(t, job, false)
			if silent < domain.TroubleshootingIdle-time.Minute {
				t.Fatalf("a proxy cut the silent session after %s: %q", silent, reason)
			}
			time.Sleep(time.Second) // for the agent to free the container's slot

			b.login("owner@example.test", x.f.password)
			opened = x.open(t, b, x.app, 1, 201)
			peer = x.attachBrowser(t, b, opened)
			job = a.startJob(t, peer, 4322)
			began := time.Now()
			typing := time.NewTicker(10 * time.Minute)
			defer typing.Stop()
			deadline := time.After(domain.TroubleshootingMaxDuration + time.Minute)
		kept:
			for {
				select {
				case _, open := <-peer.frames:
					if !open {
						break kept
					}
				case <-typing.C:
					_ = peer.conn.Write(context.Background(), websocket.MessageBinary, append([]byte{terminal.Input}, "\n"...))
				case <-deadline:
					t.Fatal("the session outlived its maximum")
				}
			}
			reason = x.closeReason(t, opened.Session.Id, 20*time.Second)
			t.Logf("kept session ended after %s, %v, reason %q", time.Since(began), peer.err, reason)
			t.Logf("agent journal:\n%s", a.journal(t))
			a.waitRunning(t, job, false)
			if reason != "deadline" || time.Since(began) < 59*time.Minute {
				t.Fatalf("a proxy cut the session: %s, %q", time.Since(began), reason)
			}
		})
	}
}
