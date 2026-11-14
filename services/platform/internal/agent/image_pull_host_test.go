package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// nativePullFixture is a TLS registry on the host, trusted by Docker and the agent through
// /etc/docker/certs.d, serving fixture/app.
type nativePullFixture struct {
	t        *testing.T
	ctx      context.Context
	docker   *client.Client
	host     string
	docs     map[string][2]string // digest: media type, content
	blobs    map[string][]byte
	overlong map[string]bool // blobs sent as themselves, then zeros without end
	large    map[string]func() io.Reader
	reads    map[string]*atomic.Int64
	sent     atomic.Int64
}

func newNativePullFixture(t *testing.T) *nativePullFixture {
	t.Helper()
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Linux host")
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	f := &nativePullFixture{t: t, ctx: context.Background(), docker: docker, docs: map[string][2]string{}, blobs: map[string][]byte{}, overlong: map[string]bool{}, large: map[string]func() io.Reader{}, reads: map[string]*atomic.Int64{}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.host = strings.TrimPrefix(server.URL, "https://")
	trust := filepath.Join("/etc/docker/certs.d", f.host)
	if err = os.Mkdir(trust, 0700); err != nil {
		t.Fatal("fixture refuses an existing trust directory", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(trust) })
	if err = os.WriteFile(filepath.Join(trust, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nativePullFixture) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v2/fixture/app/")
	kind, d, _ := strings.Cut(name, "/")
	if counter := f.reads[d]; counter != nil && r.Method == http.MethodGet {
		counter.Add(1)
	}
	switch {
	case r.URL.Path == "/v2/":
	case kind == "manifests" && f.docs[d][1] != "":
		w.Header().Set("Content-Type", f.docs[d][0])
		w.Header().Set("Docker-Content-Digest", d)
		_, _ = w.Write([]byte(f.docs[d][1]))
	case kind == "blobs" && f.overlong[d]:
		if r.Method != http.MethodGet {
			return
		}
		chunk := make([]byte, 1<<20)
		copy(chunk, f.blobs[d])
		for f.sent.Load() < 16<<30 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			f.sent.Add(int64(len(chunk)))
		}
	case kind == "blobs" && f.large[d] != nil:
		size, _ := io.Copy(io.Discard, f.large[d]())
		w.Header().Set("Content-Length", fmt.Sprint(size))
		if r.Method == http.MethodGet {
			n, _ := io.Copy(w, f.large[d]())
			f.sent.Add(n)
		}
	case kind == "blobs" && f.blobs[d] != nil:
		w.Header().Set("Content-Length", fmt.Sprint(len(f.blobs[d])))
		_, _ = w.Write(f.blobs[d])
	default:
		http.NotFound(w, r)
	}
}

// image adds a manifest for arch with one layer holding file, and returns its digest and its
// layer's.
func (f *nativePullFixture) image(arch, file string) (manifest, layer string) {
	layerTar := tarOf(f.t, testFile{file, 10})
	layerData := gzipOf(f.t, layerTar)
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux","config":{"Cmd":["/%s"]},"rootfs":{"type":"layers","diff_ids":[%q]}}`, arch, file, sha(layerTar))
	layer = sha(layerData)
	f.blobs[layer], f.blobs[sha(config)] = layerData, config
	content := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[{"mediaType":%q,"digest":%q,"size":%d}]}`, ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageConfig, sha(config), len(config), ocispec.MediaTypeImageLayerGzip, layer, len(layerData))
	manifest = sha([]byte(content))
	f.docs[manifest] = [2]string{ocispec.MediaTypeImageManifest, content}
	return manifest, layer
}

// attestation adds a provenance attestation for manifest, as BuildKit pushes one, and returns
// its index entry.
func (f *nativePullFixture) attestation(manifest string) string {
	config := []byte(`{"architecture":"unknown","os":"unknown","config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	statement := []byte(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://slsa.dev/provenance/v0.2","subject":[],"predicate":{}}`)
	f.blobs[sha(config)], f.blobs[sha(statement)] = config, statement
	content := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.in-toto+json","digest":%q,"size":%d,"annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v0.2"}}]}`, ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageConfig, sha(config), len(config), sha(statement), len(statement))
	d := sha([]byte(content))
	f.docs[d] = [2]string{ocispec.MediaTypeImageManifest, content}
	return fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.digest":%q,"vnd.docker.reference.type":"attestation-manifest"}}`, ocispec.MediaTypeImageManifest, d, len(content), manifest)
}

func (f *nativePullFixture) index(entries map[string]string, extra ...string) string {
	var list []string
	for arch, manifest := range entries {
		list = append(list, fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":"linux","architecture":%q}}`, ocispec.MediaTypeImageManifest, manifest, len(f.docs[manifest][1]), arch))
	}
	list = append(list, extra...)
	content := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[%s]}`, ocispec.MediaTypeImageIndex, strings.Join(list, ","))
	d := sha([]byte(content))
	f.docs[d] = [2]string{ocispec.MediaTypeImageIndex, content}
	return d
}

// leastFree runs fn and returns how much of Docker's storage it took at most.
func (f *nativePullFixture) leastFree(fn func()) int64 {
	f.t.Helper()
	return storageTaken(f.t, f.ctx, f.docker, fn)
}

// storageTaken runs fn and returns how much of Docker's storage it took at most while it ran.
func storageTaken(t *testing.T, ctx context.Context, docker *client.Client, fn func()) int64 {
	t.Helper()
	roots, ok := imageStorageRoots(ctx, docker)
	before, _, known := storageFree(roots)
	if !ok || !known {
		t.Fatal("cannot observe Docker storage")
	}
	var lowest atomic.Int64
	lowest.Store(before)
	stop, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if free, _, ok := storageFree(roots); ok && free < lowest.Load() {
				lowest.Store(free)
			}
		}
	}()
	fn()
	close(stop)
	<-watched
	return before - lowest.Load()
}

// A registry that sends a blob without end, or a blob other than its digest, is refused before
// Docker takes more of its storage than the blob's descriptor says.
func TestNativePullRefusesBlobsUnlikeTheirDescriptors(t *testing.T) {
	f := newNativePullFixture(t)
	for name, tc := range map[string]struct {
		file, note string
		spoil      func(layer string)
	}{
		"a blob without end": {"endless", "sent more for a blob than the image's manifest says", func(layer string) { f.overlong[layer] = true }},
		"a blob that differs from its digest": {"spoiled", "differs from its digest", func(layer string) {
			spoiled := append([]byte{}, f.blobs[layer]...)
			// Still valid gzip: the byte is its header's modification time.
			spoiled[4] ^= 1
			f.blobs[layer] = spoiled
		}},
	} {
		t.Run(name, func(t *testing.T) {
			manifest, layer := f.image(runtime.GOARCH, tc.file)
			tc.spoil(layer)
			image := f.host + "/fixture/app@" + manifest
			var err error
			used := f.leastFree(func() { err = (&Engine{Docker: f.docker}).pullMeasuredImage(f.ctx, image, nil) })
			var refused *imagePullRefused
			if !errors.As(err, &refused) || !strings.Contains(refused.note, tc.note) {
				t.Fatal("not refused for its reason", err)
			}
			if used > 64<<20 {
				t.Fatalf("refusing it took %d MiB of Docker storage", used>>20)
			}
			if _, err = f.docker.ImageInspect(f.ctx, image); !errdefs.IsNotFound(err) {
				t.Fatal("the refused image is in Docker", err)
			}
		})
	}
}

// Where Docker pulls itself, with its classic image store, the agent stops a pull once Docker's
// storage loses more than the image measured and its slack, as a registry sending more than
// the image would make it.
func TestNativePullWatchStopsAPullBeyondItsMeasure(t *testing.T) {
	f := newNativePullFixture(t)
	// An image whose one layer is a valid 1.5 GiB tar of zeros, of its own so Docker has none
	// of it yet. The agent measured it as 1 MiB, as if its registry sent more than it named;
	// Docker checks a blob's length as it reads it, so a registry can't be made to here.
	name := "watched-" + fmt.Sprint(time.Now().UnixNano())
	var header bytes.Buffer
	w := tar.NewWriter(&header)
	_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: 1536 << 20})
	_ = w.Flush()
	layer := func() io.Reader {
		return io.MultiReader(bytes.NewReader(header.Bytes()), io.LimitReader(zeros{}, 1536<<20+1024))
	}
	digester := sha256.New()
	size, _ := io.Copy(digester, layer())
	layerDigest := "sha256:" + hex.EncodeToString(digester.Sum(nil))
	f.large[layerDigest] = layer
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux","rootfs":{"type":"layers","diff_ids":[%q]}}`, runtime.GOARCH, layerDigest)
	f.blobs[sha(config)] = config
	content := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[{"mediaType":%q,"digest":%q,"size":%d}]}`, ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageConfig, sha(config), len(config), ocispec.MediaTypeImageLayer, layerDigest, size)
	manifest := sha([]byte(content))
	f.docs[manifest] = [2]string{ocispec.MediaTypeImageManifest, content}
	image := f.host + "/fixture/app@" + manifest
	var err error
	used := f.leastFree(func() {
		err = (&Engine{Docker: f.docker}).pullWatched(f.ctx, image, "", imageCost{Bytes: 1 << 20}, imageLoadLimit{reserveBytes: DefaultImageLoadReserveBytes})
	})
	var refused *imageLoadRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.why, "so the agent stopped it") {
		t.Fatal("a pull beyond its measure was not stopped", err)
	}
	// The slack, and what Docker writes before the agent next looks and Docker stops.
	if used > pullSlack+768<<20 {
		t.Fatalf("the stopped pull took %d MiB of Docker storage; the registry sent %d MiB", used>>20, f.sent.Load()>>20)
	}
	if f.sent.Load() >= size {
		t.Fatal("Docker read the whole layer before the agent stopped it")
	}
	if _, err = f.docker.ImageInspect(f.ctx, image); !errdefs.IsNotFound(err) {
		t.Fatal("the stopped pull left an image", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A multi-platform image reaches Docker as Docker's own pull of it would: under the reference
// the deployment uses, with the same ID, for this server's platform, runnable without a pull.
// With the containerd image store, Docker never reads the registry; the agent reads each blob
// once.
func TestNativePullPutsAMultiPlatformImageInDocker(t *testing.T) {
	f := newNativePullFixture(t)
	other := map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]
	ours, ourLayer := f.image(runtime.GOARCH, "ours")
	theirs, theirLayer := f.image(other, "theirs")
	// BuildKit pushes a provenance attestation for each image by default.
	root := f.index(map[string]string{runtime.GOARCH: ours, other: theirs}, f.attestation(ours), f.attestation(theirs))
	for _, d := range []string{ourLayer, theirLayer} {
		f.reads[d] = &atomic.Int64{}
	}
	image := f.host + "/fixture/app@" + root
	info, err := f.docker.Info(f.ctx, client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	containerd := containerdImageStore(info.Info.DriverStatus)
	if err = (&Engine{Docker: f.docker}).pullMeasuredImage(f.ctx, image, nil); err != nil {
		t.Fatal(err)
	}
	if f.reads[theirLayer].Load() != 0 {
		t.Fatal("another platform's layer was read")
	}
	if reads := f.reads[ourLayer].Load(); containerd && reads != 1 || reads < 1 {
		t.Fatalf("this server's layer was read %d times", reads)
	}
	loaded, err := f.docker.ImageInspect(f.ctx, image)
	if err != nil || loaded.Architecture != runtime.GOARCH {
		t.Fatal("the image is not in Docker for this server", err)
	}
	created, err := f.docker.ContainerCreate(f.ctx, client.ContainerCreateOptions{Config: &container.Config{Image: image}})
	if err != nil {
		t.Fatal("the image does not run without a pull", err)
	}
	_, _ = f.docker.ContainerRemove(f.ctx, created.ID, client.ContainerRemoveOptions{Force: true})
	if _, err = f.docker.ImageRemove(f.ctx, image, client.ImageRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if err = (&Engine{Docker: f.docker}).pullImage(f.ctx, image); err != nil {
		t.Fatal("Docker's own pull", err)
	}
	defer f.docker.ImageRemove(f.ctx, image, client.ImageRemoveOptions{Force: true})
	pulled, err := f.docker.ImageInspect(f.ctx, image)
	if err != nil || pulled.ID != loaded.ID || strings.Join(pulled.RepoDigests, ",") != strings.Join(loaded.RepoDigests, ",") || strings.Join(pulled.RepoTags, ",") != strings.Join(loaded.RepoTags, ",") || pulled.Architecture != loaded.Architecture {
		t.Fatalf("Docker's own pull differs: %s %v %v, the agent's %s %v %v", pulled.ID, pulled.RepoDigests, pulled.RepoTags, loaded.ID, loaded.RepoDigests, loaded.RepoTags)
	}
}
