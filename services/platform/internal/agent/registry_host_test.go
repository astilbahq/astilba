package agent

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type nativeRegistry struct {
	repository string
	ca         []byte
	push, pull string
}

// Zot is pinned and installed by the disposable-host build-tools fixture.
func startNativeRegistry(t *testing.T) *nativeRegistry {
	t.Helper()
	binary := "/opt/astilba-plat5/bin/zot"
	if _, err := os.Stat(binary); err != nil {
		t.Fatal("install the pinned disposable registry fixture first", err)
	}
	dir := t.TempDir()
	passwords := map[string]string{"builder": "builder-fixture-password", "deployer": "deployer-fixture-password"}
	var hashes strings.Builder
	for user, password := range passwords {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&hashes, "%s:%s\n", user, hash)
	}
	if err := os.WriteFile(filepath.Join(dir, "htpasswd"), []byte(hashes.String()), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	config := map[string]any{"distSpecVersion": "1.1.1", "storage": map[string]any{"rootDirectory": filepath.Join(dir, "data")}, "http": map[string]any{"address": "127.0.0.1", "port": port, "auth": map[string]any{"htpasswd": map[string]any{"path": filepath.Join(dir, "htpasswd")}}, "accessControl": map[string]any{"repositories": map[string]any{"fixture/**": map[string]any{"policies": []any{map[string]any{"users": []string{"builder"}, "actions": []string{"read", "create", "update"}}, map[string]any{"users": []string{"deployer"}, "actions": []string{"read"}}}}}}}, "log": map[string]any{"level": "error"}}
	raw, _ := json.Marshal(config)
	file := filepath.Join(dir, "config.json")
	_ = os.WriteFile(file, raw, 0600)
	log, err := os.Create(filepath.Join(dir, "registry.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "serve", file)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	backend := "http://127.0.0.1:" + port
	ready := false
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequest("GET", backend+"/v2/", nil)
		req.SetBasicAuth("builder", passwords["builder"])
		client := &http.Client{Timeout: time.Second}
		response, err := client.Do(req)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		raw, _ := os.ReadFile(filepath.Join(dir, "registry.log"))
		t.Fatalf("registry unavailable: %s", raw)
	}
	target, _ := url.Parse(backend)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var origin string
	proxy.ModifyResponse = func(response *http.Response) error {
		if location := response.Header.Get("Location"); location != "" {
			u, err := url.Parse(location)
			if err != nil {
				return err
			}
			if u.Host != "" {
				endpoint, _ := url.Parse(origin)
				u.Scheme = endpoint.Scheme
				u.Host = endpoint.Host
				response.Header.Set("Location", u.String())
			}
		}
		return nil
	}
	server := httptest.NewTLSServer(proxy)
	origin = server.URL
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	address := strings.TrimPrefix(origin, "https://")
	trust := filepath.Join("/etc/docker/certs.d", address)
	if err = os.MkdirAll(filepath.Dir(trust), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(trust, 0700); err != nil {
		t.Fatal("fixture refuses an existing trust directory", err)
	}
	certFile := filepath.Join(trust, "ca.crt")
	if err = os.WriteFile(certFile, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, err := os.ReadFile(certFile)
		if err == nil && string(current) == string(certificate) {
			_ = os.Remove(certFile)
			_ = os.Remove(trust)
		}
	})
	return &nativeRegistry{repository: address + "/fixture/application", ca: certificate, push: passwords["builder"], pull: passwords["deployer"]}
}
func appendNativeCA(t *testing.T, engine *Engine, container string, ca []byte) {
	t.Helper()
	_, err := engine.pipelineCLI(context.Background(), container, nil, ca, 1024, "sh", "-ec", "cat >> /etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		t.Fatal("cannot configure disposable registry CA", err)
	}
}
