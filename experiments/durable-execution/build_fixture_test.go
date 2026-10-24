// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const buildTools = "/opt/astilba-plat5/bin/"
const buildkitImage = "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"

type buildCertificates struct {
	ca, server, serverKey, client, clientKey []byte
	roots                                    *x509.CertPool
}

func buildPKI(t *testing.T, ip net.IP) buildCertificates {
	t.Helper()
	makeKey := func() *ecdsa.PrivateKey {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		return k
	}
	caKey := makeKey()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable build fixture CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	result := buildCertificates{ca: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), roots: x509.NewCertPool()}
	result.roots.AppendCertsFromPEM(result.ca)
	issue := func(serial int64, client bool) ([]byte, []byte) {
		k := makeKey()
		usage := x509.ExtKeyUsageServerAuth
		if client {
			usage = x509.ExtKeyUsageClientAuth
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Disposable build endpoint"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), ip}}
		d, e := x509.CreateCertificate(rand.Reader, leaf, ca, &k.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		key, e := x509.MarshalECPrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key})
	}
	result.server, result.serverKey = issue(2, false)
	result.client, result.clientKey = issue(3, true)
	return result
}

func buildWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func buildEnv(config string) []string {
	return []string{"PATH=" + buildTools + ":" + hostTools + "/bin:/usr/bin:/bin", "HOME=" + config, "DOCKER_HOST=" + hostSocket, "DOCKER_CONFIG=" + config}
}
func buildExec(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, buildTools+name, args...)
	c.Env = env
	return c.CombinedOutput()
}

type buildRegistry struct {
	address, builderConfig string
	client                 *http.Client
	passwords              map[string]string
	interrupt              atomic.Bool
	interrupted            chan struct{}
}

func startBuildRegistry(t *testing.T, h *runtimeHarness, gateway string, pki buildCertificates) *buildRegistry {
	t.Helper()
	r := &buildRegistry{passwords: map[string]string{"builder": newCredential(), "deployer": newCredential()}, interrupted: make(chan struct{}, 1)}
	dir := filepath.Join(h.dir, "registry")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	var hashes strings.Builder
	for _, user := range []string{"builder", "deployer"} {
		hash, e := bcrypt.GenerateFromPassword([]byte(r.passwords[user]), bcrypt.MinCost)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&hashes, "%s:%s\n", user, hash)
	}
	buildWrite(t, filepath.Join(dir, "htpasswd"), []byte(hashes.String()))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	backend := "http://127.0.0.1:" + strconv.Itoa(port)
	config := map[string]any{"distSpecVersion": "1.1.1", "storage": map[string]any{"rootDirectory": filepath.Join(dir, "data")}, "http": map[string]any{"address": "127.0.0.1", "port": strconv.Itoa(port), "auth": map[string]any{"htpasswd": map[string]any{"path": filepath.Join(dir, "htpasswd")}}, "accessControl": map[string]any{"repositories": map[string]any{"fixture/**": map[string]any{"policies": []any{map[string]any{"users": []string{"builder"}, "actions": []string{"read", "create", "update"}}, map[string]any{"users": []string{"deployer"}, "actions": []string{"read"}}}}}}}, "log": map[string]any{"level": "error"}}
	hostWriteJSON(t, filepath.Join(dir, "config.json"), config)
	log, e := os.Create(filepath.Join(dir, "zot.log"))
	if e != nil {
		t.Fatal(e)
	}
	process := exec.Command(buildTools+"zot", "serve", filepath.Join(dir, "config.json"))
	process.Env = buildEnv(dir)
	process.Stdout = log
	process.Stderr = log
	if e := process.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { process.Process.Kill(); process.Wait(); log.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := hostAwait(ctx, func() error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, backend+"/v2/", nil)
		req.SetBasicAuth("builder", r.passwords["builder"])
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("registry readiness status %d", resp.StatusCode)
		}
		return nil
	}); e != nil {
		data, _ := os.ReadFile(filepath.Join(dir, "zot.log"))
		t.Fatalf("registry readiness: %v: %s", e, data)
	}
	target, _ := url.Parse(backend)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if loc := resp.Header.Get("Location"); loc != "" {
			u, e := url.Parse(loc)
			if e != nil {
				return e
			}
			if u.Host != "" {
				u.Scheme = "https"
				u.Host = r.address
				resp.Header.Set("Location", u.String())
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "fixture upstream unavailable", http.StatusBadGateway)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.interrupt.Load() && strings.Contains(req.URL.Path, "/blobs/uploads/") && (req.Method == http.MethodPatch || req.Method == http.MethodPut) {
			if n, _ := io.CopyN(io.Discard, req.Body, 32); n > 0 {
				select {
				case r.interrupted <- struct{}{}:
				default:
				}
				conn, _, e := http.NewResponseController(w).Hijack()
				if e == nil {
					conn.Close()
				}
				return
			}
		}
		proxy.ServeHTTP(w, req)
	})
	gatewayListener, e := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = gatewayListener
	certificate, e := tls.X509KeyPair(pki.server, pki.serverKey)
	if e != nil {
		t.Fatal(e)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	r.address = strings.TrimPrefix(server.URL, "https://")
	r.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pki.roots, MinVersion: tls.VersionTLS12}}, Timeout: 5 * time.Second}
	t.Cleanup(r.client.CloseIdleConnections)
	// Docker's registry trust directory is host-scoped. Never replace an existing entry.
	if e := os.MkdirAll("/etc/docker/certs.d", 0755); e != nil {
		t.Fatal(e)
	}
	trust := filepath.Join("/etc/docker/certs.d", r.address)
	if e := os.Mkdir(trust, 0700); e != nil {
		t.Fatal(e)
	}
	buildWrite(t, filepath.Join(trust, "ca.crt"), pki.ca)
	t.Cleanup(func() { os.RemoveAll(trust) })
	auth := func(dir, user string) {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		hostWriteJSON(t, filepath.Join(dir, "config.json"), map[string]any{"auths": map[string]any{r.address: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + r.passwords[user]))}}})
	}
	r.builderConfig = filepath.Join(h.dir, "builder-auth")
	auth(r.builderConfig, "builder")
	auth(h.config, "deployer")
	return r
}

func (r *buildRegistry) request(t *testing.T, method, path, user string, body io.Reader) *http.Response {
	t.Helper()
	req, e := http.NewRequest(method, "https://"+r.address+path, body)
	if e != nil {
		t.Fatal(e)
	}
	if user != "" {
		req.SetBasicAuth(user, r.passwords[user])
	}
	resp, e := r.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	return resp
}

type buildWorker struct {
	id, address, dir string
	remote           bool
}

func startBuildWorker(t *testing.T, h *runtimeHarness, r *buildRegistry, pki buildCertificates, network, name string, remote bool, space int) buildWorker {
	t.Helper()
	dir := filepath.Join(h.dir, name)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for file, data := range map[string][]byte{"ca.pem": pki.ca, "server.pem": pki.server, "server.key": pki.serverKey} {
		buildWrite(t, filepath.Join(dir, file), data)
	}
	config := "[registry.\"" + r.address + "\"]\n  ca = [\"/fixture/ca.pem\"]\n"
	if remote {
		config += "[grpc]\n  address = [\"tcp://0.0.0.0:1234\"]\n[grpc.tls]\n  cert = \"/fixture/server.pem\"\n  key = \"/fixture/server.key\"\n  ca = \"/fixture/ca.pem\"\n"
	}
	buildWrite(t, filepath.Join(dir, "buildkitd.toml"), []byte(config))
	args := []string{"run", "--detach", "--name", name, "--privileged", "--network", network, "--label", hostOwnerLabel + "=" + h.run, "--label", hostProjectLabel + "=builds", "--memory", fmt.Sprintf("%dm", space+512), "--cpus", "2", "--tmpfs", fmt.Sprintf("/var/lib/buildkit:rw,exec,nosuid,size=%dm", space), "--volume", dir + ":/fixture:ro"}
	if remote {
		args = append(args, "--publish", "127.0.0.1::1234")
	}
	args = append(args, buildkitImage, "--config", "/fixture/buildkitd.toml")
	id := strings.TrimSpace(string(h.command(t, nil, args...)))
	clientDir := filepath.Join(h.dir, name+"-client")
	if e := os.MkdirAll(clientDir, 0700); e != nil {
		t.Fatal(e)
	}
	for file, data := range map[string][]byte{"ca.pem": pki.ca, "client.pem": pki.client, "client.key": pki.clientKey} {
		buildWrite(t, filepath.Join(clientDir, file), data)
	}
	w := buildWorker{id: id, dir: clientDir, remote: remote, address: "docker-container://" + id}
	if remote {
		out := strings.TrimSpace(string(h.command(t, nil, "port", id, "1234/tcp")))
		w.address = "tcp://" + out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e := hostAwait(ctx, func() error { _, e := w.command(ctx, r.builderConfig, "debug", "workers"); return e }); e != nil {
		t.Fatalf("BuildKit readiness: %v: %s", e, h.command(t, nil, "logs", id))
	}
	return w
}
func (w buildWorker) command(ctx context.Context, config string, args ...string) ([]byte, error) {
	global := []string{"--addr", w.address}
	if w.remote {
		global = append(global, "--tlscacert", filepath.Join(w.dir, "ca.pem"), "--tlscert", filepath.Join(w.dir, "client.pem"), "--tlskey", filepath.Join(w.dir, "client.key"))
	}
	return buildExec(ctx, buildEnv(config), "buildctl", append(global, args...)...)
}
func (w buildWorker) build(ctx context.Context, config, source, secret, output, metadata string, opts ...string) ([]byte, error) {
	args := []string{"build", "--frontend", "dockerfile.v0", "--local", "context=" + source, "--local", "dockerfile=" + source, "--opt", "platform=" + "linux/" + runtime.GOARCH, "--secret", "id=build,src=" + secret, "--output", output, "--metadata-file", metadata, "--progress", "plain"}
	for _, option := range opts {
		args = append(args, "--opt", option)
	}
	return w.command(ctx, config, args...)
}

func buildDigest(t *testing.T, path string) string {
	t.Helper()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		t.Fatal("invalid build metadata")
	}
	v, _ := m["containerimage.digest"].(string)
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		t.Fatal("missing immutable build digest")
	}
	return v
}
func requireBuild(t *testing.T, w buildWorker, config, source, secret, output, metadata string, opts ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, e := w.build(ctx, config, source, secret, output, metadata, opts...)
	if e != nil {
		t.Fatalf("build failed: %v: %s", e, out)
	}
	return out
}
