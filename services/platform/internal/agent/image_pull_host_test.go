package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// A registry can send Docker more than the image it names: Docker writes a blob as it arrives
// and checks its size only at the end. The agent measures the image it names, then stops the
// pull once Docker's storage loses more than that and its slack.
func TestNativePullStopsARegistrySendingMoreThanItsImage(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	digestOf := func(data []byte) string {
		sum := sha256.Sum256(data)
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	layerTar := tarOf(t, testFile{"app", 10})
	layer := gzipOf(t, layerTar)
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux","rootfs":{"type":"layers","diff_ids":[%q]}}`, runtime.GOARCH, digestOf(layerTar))
	manifest := fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[{"mediaType":%q,"digest":%q,"size":%d}]}`, ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageConfig, digestOf(config), len(config), ocispec.MediaTypeImageLayerGzip, digestOf(layer), len(layer))
	var layerReads, sent atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
		case "/v2/fixture/app/manifests/" + digestOf(manifest):
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", digestOf(manifest))
			_, _ = w.Write(manifest)
		case "/v2/fixture/app/blobs/" + digestOf(config):
			_, _ = w.Write(config)
		case "/v2/fixture/app/blobs/" + digestOf(layer):
			if r.Method != http.MethodGet || layerReads.Add(1) == 1 {
				// The agent's own read gets the layer the manifest names.
				w.Header().Set("Content-Length", fmt.Sprint(len(layer)))
				_, _ = w.Write(layer)
				return
			}
			// Docker's gets the layer, then zeros without end, sent without a length.
			flusher := w.(http.Flusher)
			chunk := make([]byte, 1<<20)
			copy(chunk, layer)
			for sent.Load() < 16<<30 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flusher.Flush()
				sent.Add(int64(len(chunk)))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	trust := filepath.Join("/etc/docker/certs.d", host)
	if err = os.Mkdir(trust, 0700); err != nil {
		t.Fatal("fixture refuses an existing trust directory", err)
	}
	defer os.RemoveAll(trust)
	if err = os.WriteFile(filepath.Join(trust, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	image := host + "/fixture/app@" + digestOf(manifest)
	roots, ok := imageStorageRoots(ctx, docker)
	before, _, known := storageFree(roots)
	if !ok || !known {
		t.Fatal("cannot observe Docker storage")
	}
	var lowest atomic.Int64
	lowest.Store(before)
	stopWatching, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stopWatching:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if free, _, ok := storageFree(roots); ok && free < lowest.Load() {
				lowest.Store(free)
			}
		}
	}()
	engine := &Engine{Docker: docker}
	err = engine.pullMeasuredImage(ctx, image, nil)
	close(stopWatching)
	<-watched
	var refused *imagePullRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.note, "so the agent stopped it") {
		t.Fatal("a registry sending more than its image was not stopped", err)
	}
	if layerReads.Load() < 2 {
		t.Fatal("Docker never read the layer")
	}
	// The pull may take the measured image, the slack, and what Docker writes before the agent
	// next looks and Docker stops.
	if used := before - lowest.Load(); used > pullSlack+768<<20 {
		t.Fatalf("the stopped pull took %d MiB of Docker storage; the registry sent %d MiB", used>>20, sent.Load()>>20)
	}
	if _, err = docker.ImageInspect(ctx, image); !errdefs.IsNotFound(err) {
		t.Fatal("the stopped pull left an image", err)
	}
	t.Logf("stopped after the registry sent %d MiB; Docker's storage lost at most %d MiB", sent.Load()>>20, (before-lowest.Load())>>20)
}
