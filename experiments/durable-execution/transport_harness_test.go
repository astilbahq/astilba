// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const proxyImage = "traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953"

//go:embed transport_proxy.yml
var proxyConfig []byte

const proxyRoutes = `http:
  routers:
    agent:
      rule: PathPrefix(` + "`/`" + `)
      entryPoints: [https]
      service: controller
      tls: {}
  services:
    controller:
      loadBalancer:
        servers:
          - url: http://controller:8080
tls:
  stores:
    default:
      defaultCertificate:
        certFile: /fixture/cert.pem
        keyFile: /fixture/key.pem
  certificates:
    - certFile: /fixture/cert.pem
      keyFile: /fixture/key.pem
`

func TestTransportController(t *testing.T) {
	path := os.Getenv("PLAT3_CONTROLLER_CONFIG")
	if path == "" {
		t.Skip("container subprocess entry point")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DB      string
		Hosting map[string]string
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Hosting != nil {
		t.Setenv("PLAT6_DATABASE_URL", cfg.DB)
		for key, value := range cfg.Hosting {
			t.Setenv(key, value)
		}
		if err := RunHostingFixture(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		return
	}
	pool, err := pgxpool.New(context.Background(), cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	c := &transportController{pool: pool, polling: map[string]bool{}}
	s := &http.Server{Addr: ":8080", Handler: c, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	if err := s.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

type transportHarness struct {
	*fixture
	client            *http.Client
	url               string
	controller        string
	proxy             string
	replaceController func()
}

func fixtureCertificate(t *testing.T) ([]byte, []byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Astilba disposable fixture"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("invalid fixture certificate")
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encodedKey}), roots
}

func copyFixtureFiles(t *testing.T, h *harness, id string, files map[string][]byte) {
	t.Helper()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	if err := w.WriteHeader(&tar.Header{Name: "fixture/", Mode: 0o700, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		mode := int64(0o600)
		if name == "controller" {
			mode = 0o700
		}
		if err := w.WriteHeader(&tar.Header{Name: "fixture/" + name, Size: int64(len(data)), Mode: mode}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.docker.CopyToContainer(context.Background(), id, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive}); err != nil {
		t.Fatal(err)
	}
}

func newTransportHarness(t *testing.T) *transportHarness {
	return newHTTPSHarness(t, nil)
}

func newHTTPSHarness(t *testing.T, hosting map[string]string) *transportHarness {
	t.Helper()
	h := newHarness(t)
	f := h.fixture(t)
	if _, err := f.pool.Exec(context.Background(), transportSchema); err != nil {
		t.Fatal(err)
	}
	if hosting != nil {
		if _, err := f.pool.Exec(context.Background(), hostingSchema); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO transport_servers(id,role,current_hash) VALUES ('server-01','deployment',$1)`, credentialHash(hostingAgentCredential(hosting["PLAT6_AGENT_SEED"], "server-01"))); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := h.docker.ImageInspect(ctx, proxyImage); err != nil {
		t.Fatalf("pull pinned proxy image first: %s", proxyImage)
	}
	n, err := h.docker.NetworkCreate(ctx, "plat3-"+h.run, client.NetworkCreateOptions{Internal: true, Labels: map[string]string{ownerLabel: h.run}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		state, err := h.docker.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil || state.Network.Labels[ownerLabel] != h.run {
			t.Errorf("cannot verify fixture network ownership")
			return
		}
		for id := range state.Network.Containers {
			c, err := h.docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if err != nil || c.Container.Config.Labels[ownerLabel] != h.run {
				t.Errorf("cannot verify network endpoint ownership")
				continue
			}
			if _, err := h.docker.NetworkDisconnect(ctx, n.ID, client.NetworkDisconnectOptions{Container: id, Force: true}); err != nil {
				t.Errorf("disconnect: %v", err)
			}
		}
		if _, err := h.docker.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
			t.Errorf("remove fixture network: %v", err)
		}
	})
	if _, err := h.docker.NetworkConnect(ctx, n.ID, client.NetworkConnectOptions{Container: "plat2-postgres-" + h.run, EndpointConfig: &network.EndpointSettings{Aliases: []string{"database"}}}); err != nil {
		t.Fatal(err)
	}
	version, err := h.docker.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "controller")
	build := exec.Command("go", "test", "-c", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+version.Arch, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controller: %v: %s", err, output)
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(f.cfg.DB)
	u.Host = "database:5432"
	cert, key, roots := fixtureCertificate(t)
	port := network.MustParsePort("8443/tcp")
	proxy, err := h.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Image: proxyImage,
		Config: &container.Config{Cmd: []string{"--configFile=/fixture/traefik.yml"}, Labels: map[string]string{ownerLabel: h.run}, ExposedPorts: network.PortSet{port: {}}},
		HostConfig: &container.HostConfig{NetworkMode: "bridge", CapDrop: []string{"ALL"},
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	copyFixtureFiles(t, h, proxy.ID, map[string][]byte{"traefik.yml": proxyConfig, "routes.yml": []byte(proxyRoutes), "cert.pem": cert, "key.pem": key})
	if _, err := h.docker.NetworkConnect(ctx, n.ID, client.NetworkConnectOptions{Container: proxy.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.docker.ContainerStart(ctx, proxy.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, err := h.docker.ContainerInspect(ctx, proxy.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	t.Cleanup(transport.CloseIdleConnections)
	x := &transportHarness{fixture: f, client: &http.Client{Transport: transport, Timeout: 35 * time.Second},
		url: "https://127.0.0.1:" + inspected.Container.NetworkSettings.Ports[port][0].HostPort, proxy: proxy.ID}
	if hosting != nil {
		hosting["PLAT6_PUBLIC_ORIGIN"] = x.url
	}
	cfg, _ := json.Marshal(struct {
		DB      string
		Hosting map[string]string
	}{u.String(), hosting})
	x.replaceController = func() {
		if x.controller != "" {
			old, err := h.docker.ContainerInspect(ctx, x.controller, client.ContainerInspectOptions{})
			if err != nil || old.Container.Config.Labels[ownerLabel] != h.run {
				t.Fatal("cannot verify controller ownership")
			}
			if _, err := h.docker.ContainerRemove(ctx, x.controller, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Fatal(err)
			}
		}
		controller, err := h.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Image: effectImage,
			Config: &container.Config{Cmd: []string{"/fixture/controller", "-test.run=^TestTransportController$", "-test.v"},
				Env: []string{"PLAT3_CONTROLLER_CONFIG=/fixture/config.json"}, Labels: map[string]string{ownerLabel: h.run}},
			HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(n.ID), CapDrop: []string{"ALL"}},
			NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{n.ID: {Aliases: []string{"controller"}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		x.controller = controller.ID
		copyFixtureFiles(t, h, x.controller, map[string][]byte{"controller": binary, "config.json": cfg})
		if _, err := h.docker.ContainerStart(ctx, x.controller, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	x.replaceController()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, id := range []string{x.controller, proxy.ID} {
			logs, err := h.docker.ContainerLogs(context.Background(), id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "30"})
			if err == nil {
				data, _ := io.ReadAll(logs)
				logs.Close()
				t.Logf("fixture process: %s", data)
			}
		}
	})
	var readiness string
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("last HTTPS readiness result: %s", readiness)
		}
	})
	readyClient := *x.client
	readyClient.Timeout = 2 * time.Second
	wait(t, 15*time.Second, func() bool {
		r, err := readyClient.Get(x.url + "/health")
		if err != nil {
			readiness = err.Error()
			return false
		}
		r.Body.Close()
		readiness = fmt.Sprint(r.StatusCode)
		return r.StatusCode == 204
	})
	return x
}

func (h *transportHarness) issue(t *testing.T, id, role string) string {
	t.Helper()
	tx, err := h.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(context.Background(), `INSERT INTO transport_servers(id,role) VALUES($1,$2)
 ON CONFLICT(id) DO UPDATE SET current_hash=NULL,previous_hash=NULL,previous_until=NULL,revoked=false`, id, role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `DELETE FROM transport_enrollments WHERE server_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	token := newCredential()
	if _, err := tx.Exec(context.Background(), `INSERT INTO transport_enrollments SELECT $1,$2,clock_timestamp()+shift+interval '5 minutes',false FROM transport_clock`, credentialHash(token), id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return token
}

func (h *transportHarness) enroll(t *testing.T, id, role string) *transportAgent {
	t.Helper()
	token := h.issue(t, id, role)
	var identity agentIdentity
	status := h.request(t, "/v1/enroll", "", struct{ ServerID, Token string }{id, token}, &identity)
	if status != 200 || identity.ServerID != id || identity.Current == "" {
		t.Fatalf("enrollment returned status %d or wrong identity", status)
	}
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(path, identity); err != nil {
		t.Fatal(err)
	}
	return &transportAgent{client: h.client, baseURL: h.url, identity: identity, path: path}
}

func (h *transportHarness) request(t *testing.T, path, credential string, body, out any) int {
	t.Helper()
	status, err := h.sendRequest(path, credential, body, out)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (h *transportHarness) sendRequest(path, credential string, body, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, h.url+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return 0, err
		}
	}
	return resp.StatusCode, nil
}

func (h *transportHarness) command(t *testing.T, id string) envelope {
	t.Helper()
	c := h.c
	c.ID, c.Environment = token(), token()
	e := envelope{Version: 1, ServerID: id, Kind: "fixture.create", Command: c}
	h.queueCommand(t, e)
	return e
}

func (h *transportHarness) queueCommand(t *testing.T, e envelope) {
	t.Helper()
	data, _ := json.Marshal(e)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO transport_commands(id,server_id,payload) VALUES($1,$2,$3)`, e.Command.ID, e.ServerID, data); err != nil {
		t.Fatal(err)
	}
}

func (h *transportHarness) fault(t *testing.T, id, phase string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO transport_faults(server_id,phase) VALUES($1,$2)`, id, phase); err != nil {
		t.Fatal(err)
	}
}

func reloadIdentity(t *testing.T, a *transportAgent) *transportAgent {
	t.Helper()
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	next := *a
	if err := json.Unmarshal(data, &next.identity); err != nil {
		t.Fatal(err)
	}
	return &next
}

func (h *transportHarness) shift(t *testing.T, duration string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `UPDATE transport_clock SET shift=$1::interval`, duration); err != nil {
		t.Fatal(err)
	}
}

func mustPost(t *testing.T, a *transportAgent, action string, body, out any, expected int) {
	t.Helper()
	status, err := a.post(context.Background(), action, a.identity.Current, body, out)
	if status != expected || (expected < 300 && err != nil) {
		t.Fatalf("%s: status %d, expected %d (error=%v)", action, status, expected, err)
	}
}

func noPoint(string) {}

func (h *transportHarness) eventCount(t *testing.T, id, kind string) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM transport_events WHERE server_id=$1 AND kind=$2`, id, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func startTransportAgent(t *testing.T, a *transportAgent) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

func assertIdentityPrivate(t *testing.T, a *transportAgent) {
	t.Helper()
	info, err := os.Stat(a.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("identity file must be mode 0600")
	}
	files, err := os.ReadDir(filepath.Dir(a.path))
	if err != nil || len(files) != 1 || !strings.HasSuffix(files[0].Name(), ".json") {
		t.Fatal("unexpected leftover credential files")
	}
}
