// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"time"
)

func newBuildHarness(t *testing.T) *runtimeHarness {
	t.Helper()
	if os.Getenv("PLAT5_HOST_TEST") != "1" {
		t.Skip("set PLAT5_HOST_TEST=1 on the disposable Ubuntu host")
	}
	h := newRuntimeHarness(t)
	for tool, want := range map[string]string{"buildctl": "v0.34.1", "zot": "v2.1.22", "railpack": "0.43.0"} {
		out, e := buildExec(context.Background(), buildEnv(h.config), tool, "--version")
		if e != nil || !bytes.Contains(out, []byte(want)) {
			t.Fatalf("unexpected %s version: %s", tool, out)
		}
	}
	if _, e := h.docker.ImageInspect(context.Background(), buildkitImage); e != nil {
		t.Fatal("pull pinned BuildKit image first")
	}
	return h
}

func buildContext(t *testing.T, h *runtimeHarness, body, extra string) string {
	t.Helper()
	dir := filepath.Join(h.dir, "context-"+token())
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	dockerfile := "FROM " + effectImage + "\nRUN --mount=type=secret,id=build,required=true test -s /run/secrets/build && test -z \"$RUNTIME_SECRET\" && test ! -e /run/secrets/production\n" + extra + "\nCOPY index.html /www/index.html\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n"
	buildWrite(t, filepath.Join(dir, "Dockerfile"), []byte(dockerfile))
	buildWrite(t, filepath.Join(dir, "index.html"), []byte(body))
	return dir
}

func scanBuildSecrets(t *testing.T, path string, secrets []string) {
	t.Helper()
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if header.Size > 64<<20 {
			t.Fatal("unexpectedly large fixture layer")
		}
		data, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		check := func(data []byte) {
			for _, secret := range secrets {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatal("secret found in exported image data")
				}
			}
		}
		check(data)
		if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
			gz, e := gzip.NewReader(bytes.NewReader(data))
			if e != nil {
				t.Fatal(e)
			}
			plain, e := io.ReadAll(io.LimitReader(gz, 64<<20))
			gz.Close()
			if e != nil {
				t.Fatal(e)
			}
			check(plain)
		}
	}
}

func registryManifest(t *testing.T, r *buildRegistry, reference string) (string, []byte) {
	t.Helper()
	resp := r.request(t, http.MethodGet, "/v2/"+reference, "deployer", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("manifest status %d", resp.StatusCode)
	}
	body, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	digest := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(body))
	if resp.Header.Get("Docker-Content-Digest") != digest {
		t.Fatal("registry manifest digest mismatch")
	}
	return digest, body
}

func TestBuildArtifacts(t *testing.T) {
	h := newBuildHarness(t)
	sentinel := h.project("sentinel")
	h.deploySDK(t, sentinel)
	waitPG(t, h, sentinel)
	h.command(t, nil, "exec", sentinel.db, "psql", "-X", "-U", "fixture", "-d", "fixture", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE proof(value text); INSERT INTO proof VALUES('retained');")
	appID, dbID := h.inspectID(t, sentinel.app), h.inspectID(t, sentinel.db)
	unchanged := func() {
		if h.inspectID(t, sentinel.app) != appID || h.inspectID(t, sentinel.db) != dbID {
			t.Fatal("build changed the existing workload")
		}
		if string(h.command(t, nil, "exec", sentinel.app, "wget", "-q", "-O", "-", "http://127.0.0.1:8080")) != sentinel.body {
			t.Fatal("existing application stopped responding")
		}
		if strings.TrimSpace(string(h.command(t, nil, "exec", sentinel.db, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-c", "SELECT value FROM proof"))) != "retained" {
			t.Fatal("build changed the existing database")
		}
	}
	h.projects["builds"] = true
	network := "build-" + h.run
	h.command(t, nil, "network", "create", "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=builds", network)
	gateway := strings.TrimSpace(string(h.command(t, nil, "network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", network)))
	pki := buildPKI(t, net.ParseIP(gateway))
	registry := startBuildRegistry(t, h, gateway, pki)
	local := startBuildWorker(t, h, registry, pki, network, "local-"+h.run, false, 192)
	remote := startBuildWorker(t, h, registry, pki, network, "remote-"+h.run, true, 192)
	secret, runtimeSecret := newCredential(), newCredential()
	t.Setenv("RUNTIME_SECRET", runtimeSecret)
	secretFile := filepath.Join(h.dir, "build-secret")
	buildWrite(t, secretFile, []byte(secret))
	buildWrite(t, filepath.Join(h.dir, "runtime-only.env"), []byte(runtimeSecret))
	source := buildContext(t, h, "original artifact", "")
	localImage := "plat5-" + h.run + ":local"
	localTar := filepath.Join(h.dir, "local.tar")
	localSource := buildContext(t, h, "local artifact", "")
	logs := requireBuild(t, local, registry.builderConfig, localSource, secretFile, "type=docker,name="+localImage+",dest="+localTar, filepath.Join(h.dir, "local.json"))
	h.command(t, nil, "load", "--input", localTar)
	h.trackImage(t, localImage)
	if got := strings.TrimSpace(string(h.command(t, nil, "image", "inspect", "--format", "{{.Architecture}}", localImage))); got != runtime.GOARCH {
		t.Fatal("local image architecture mismatch")
	}
	if string(h.command(t, nil, "run", "--rm", "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=builds", "--entrypoint", "cat", localImage, "/www/index.html")) != "local artifact" {
		t.Fatal("local build contents differ")
	}
	scanBuildSecrets(t, localTar, []string{secret, runtimeSecret, sentinel.secret})
	repository := "fixture/" + h.run
	tag := registry.address + "/" + repository + ":current"
	metadata := filepath.Join(h.dir, "remote.json")
	logs = append(logs, requireBuild(t, remote, registry.builderConfig, source, secretFile, "type=image,name="+tag+",push=true", metadata)...)
	digest := buildDigest(t, metadata)
	observed, manifest := registryManifest(t, registry, repository+"/manifests/"+digest)
	if observed != digest {
		t.Fatal("build and registry disagree on artifact identity")
	}
	var imageManifest struct{ Config struct{ Digest string } }
	if json.Unmarshal(manifest, &imageManifest) != nil {
		t.Fatal("invalid manifest")
	}
	resp := registry.request(t, http.MethodGet, "/v2/"+repository+"/blobs/"+imageManifest.Config.Digest, "deployer", nil)
	configBytes, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 {
		t.Fatal("image config unavailable")
	}
	var config struct{ Architecture, OS string }
	if json.Unmarshal(configBytes, &config) != nil || config.Architecture != runtime.GOARCH || config.OS != "linux" {
		t.Fatal("remote target architecture mismatch")
	}
	reference := registry.address + "/" + repository + "@" + digest
	if _, err := h.docker.ImageInspect(context.Background(), reference); !errdefs.IsNotFound(err) {
		t.Fatal("remote artifact unexpectedly exists in the deployment image store before pull")
	}
	t.Logf("BuildKit 0.34.1 remote mTLS worker produced %s for linux/%s", digest, runtime.GOARCH)
	h.command(t, nil, "pull", reference)
	h.trackImage(t, reference)
	archive := filepath.Join(h.dir, "remote.tar")
	h.command(t, nil, "image", "save", "--output", archive, reference)
	scanBuildSecrets(t, archive, []string{secret, runtimeSecret, sentinel.secret, registry.passwords["builder"], registry.passwords["deployer"]})
	for _, value := range []string{secret, runtimeSecret, sentinel.secret, registry.passwords["builder"], registry.passwords["deployer"]} {
		if bytes.Contains(logs, []byte(value)) {
			t.Fatal("build log contains a secret")
		}
	}
	container := "artifact-" + h.run
	h.command(t, nil, "run", "--detach", "--name", container, "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=builds", reference)
	ready, stopReady := context.WithTimeout(context.Background(), 10*time.Second)
	err := hostAwait(ready, func() error {
		out, err := hostCommand(ready, h.config, nil, "exec", container, "wget", "-q", "-O", "-", "http://127.0.0.1:8080")
		if err != nil {
			return err
		}
		if string(out) != "original artifact" {
			return fmt.Errorf("unexpected artifact response")
		}
		return nil
	})
	stopReady()
	if err != nil {
		t.Fatal("artifact startup:", err)
	}
	checkArtifact := func() {
		if string(h.command(t, nil, "exec", container, "wget", "-q", "-O", "-", "http://127.0.0.1:8080")) != "original artifact" {
			t.Fatal("deployed artifact changed")
		}
		unchanged()
	}
	checkArtifact()
	if !t.Run("registry_permissions_and_immutable_identity", func(t *testing.T) {
		for _, request := range []struct{ method, path, user string }{{"GET", "/v2/" + repository + "/manifests/" + digest, ""}, {"POST", "/v2/" + repository + "/blobs/uploads/", "deployer"}, {"GET", "/v2/unrelated/app/manifests/latest", "builder"}} {
			resp := registry.request(t, request.method, request.path, request.user, nil)
			resp.Body.Close()
			if resp.StatusCode != 401 && resp.StatusCode != 403 {
				t.Fatalf("unauthorized registry operation returned %d", resp.StatusCode)
			}
		}
		untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Second}
		defer untrusted.CloseIdleConnections()
		if response, err := untrusted.Get("https://" + registry.address + "/v2/"); err == nil {
			response.Body.Close()
			t.Fatal("registry accepted an untrusted server certificate")
		}
		badTLS := []string{"--addr", remote.address, "--tlscacert", filepath.Join(remote.dir, "ca.pem"), "debug", "workers"}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := buildExec(ctx, buildEnv(registry.builderConfig), "buildctl", badTLS...); e == nil {
			t.Fatal("runner accepted a client without its credential")
		}
		replacement := buildContext(t, h, "replacement artifact", "")
		requireBuild(t, remote, registry.builderConfig, replacement, secretFile, "type=image,name="+tag+",push=true", filepath.Join(h.dir, "replacement.json"))
		current, _ := registryManifest(t, registry, repository+"/manifests/current")
		if current == digest {
			t.Fatal("mutable tag did not move")
		}
		h.command(t, nil, "pull", reference)
		checkArtifact()
	}) {
		return
	}
	if !t.Run("failed_and_interrupted_builds_preserve_deployment", func(t *testing.T) {
		failed := buildContext(t, h, "unused", "RUN exit 47")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, e := remote.build(ctx, registry.builderConfig, failed, secretFile, "type=image,name="+registry.address+"/"+repository+":failed,push=true", filepath.Join(h.dir, "failed.json"))
		cancel()
		if e == nil {
			t.Fatal("failed build reported success")
		}
		failedResponse := registry.request(t, http.MethodGet, "/v2/"+repository+"/manifests/failed", "deployer", nil)
		failedResponse.Body.Close()
		if failedResponse.StatusCode != http.StatusNotFound {
			t.Fatal("failed build published a manifest")
		}
		checkArtifact()
		registry.interrupt.Store(true)
		interrupted := buildContext(t, h, "transfer should be interrupted", "")
		ctx, cancel = context.WithTimeout(context.Background(), 12*time.Second)
		out, e := remote.build(ctx, registry.builderConfig, interrupted, secretFile, "type=image,name="+registry.address+"/"+repository+":interrupted,push=true", filepath.Join(h.dir, "interrupted.json"))
		cancel()
		registry.interrupt.Store(false)
		if e == nil {
			t.Fatal("interrupted upload reported success")
		}
		select {
		case <-registry.interrupted:
		default:
			t.Fatalf("upload never reached interruption barrier: %s", out)
		}
		resp := registry.request(t, http.MethodGet, "/v2/"+repository+"/manifests/interrupted", "deployer", nil)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("interrupted upload published a manifest")
		}
		checkArtifact()
		requireBuild(t, remote, registry.builderConfig, interrupted, secretFile, "type=image,name="+registry.address+"/"+repository+":retried,push=true", filepath.Join(h.dir, "retried.json"))
		checkArtifact()
	}) {
		return
	}
	t.Run("bounded_build_disk_exhaustion", func(t *testing.T) {
		limited := startBuildWorker(t, h, registry, pki, network, "limited-"+h.run, false, 32)
		source := buildContext(t, h, "unused", "RUN dd if=/dev/zero of=/large bs=1048576 count=96")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, e := limited.build(ctx, registry.builderConfig, source, secretFile, "type=oci,dest="+filepath.Join(h.dir, "exhausted.tar"), filepath.Join(h.dir, "exhausted.json"))
		if e == nil || !bytes.Contains(bytes.ToLower(out), []byte("no space left on device")) {
			t.Fatalf("expected bounded ENOSPC: %v: %s", e, out)
		}
		checkArtifact()
	})
	_ = hex.EncodeToString
	t.Run("railpack_shell_evaluation", func(t *testing.T) { exerciseRailpack(t, h, registry, pki, network); checkArtifact() })
}
