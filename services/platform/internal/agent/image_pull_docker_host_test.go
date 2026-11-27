package agent

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	bolt "go.etcd.io/bbolt"
)

// stallingImage adds an image of its own whose one layer, of size bytes, the registry sends the
// first sent bytes of before it stops sending, holding the connection open. It returns the image,
// a channel closed once the registry has sent them, and one closed once Docker ends the request.
func (f *nativePullFixture) stallingImage(size, sent int) (image string, stalled, ended <-chan struct{}) {
	f.t.Helper()
	// Random content, so Docker has none of the layer already.
	content := make([]byte, size)
	_, _ = rand.Read(content)
	layer := append(append(tarHeader(f.t, "content", size), content...), make([]byte, (512-size%512)%512+1024)...)
	layerDigest := sha(layer)
	f.blobs[layerDigest] = layer
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux","rootfs":{"type":"layers","diff_ids":[%q]}}`, runtime.GOARCH, layerDigest)
	f.blobs[sha(config)] = config
	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[{"mediaType":%q,"digest":%q,"size":%d}]}`, ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageConfig, sha(config), len(config), ocispec.MediaTypeImageLayer, layerDigest, len(layer))
	f.docs[sha([]byte(manifest))] = [2]string{ocispec.MediaTypeImageManifest, manifest}
	stalledCh, endedCh := make(chan struct{}), make(chan struct{})
	// The registry lets go of the connection once the test ends, whether or not Docker does.
	quit := make(chan struct{})
	f.t.Cleanup(func() { close(quit) })
	var requests atomic.Int32
	f.paced[layerDigest] = func(r *http.Request, w io.Writer) {
		// A request after the first, a pull made again, gets nothing at all.
		if requests.Add(1) > 1 {
			f.t.Log("Docker asked for the layer again")
			select {
			case <-r.Context().Done():
			case <-quit:
			}
			return
		}
		defer close(endedCh)
		_, _ = w.Write(layer[:sent])
		w.(http.Flusher).Flush()
		close(stalledCh)
		select {
		case <-r.Context().Done():
		case <-quit:
		}
	}
	return f.host + "/fixture/app@" + sha([]byte(manifest)), stalledCh, endedCh
}

// tarHeader is a tar header for a regular file of size bytes named name.
func tarHeader(t *testing.T, name string, size int) []byte {
	t.Helper()
	var header bytes.Buffer
	w := tar.NewWriter(&header)
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(size)}); err != nil {
		t.Fatal(err)
	}
	_ = w.Flush()
	return header.Bytes()
}

// A registry that stops sending a layer mid-way ends Docker's pull as stalled within the floor's
// windows, and its owner cancelling the command ends it within a second. Neither leaves the image
// in Docker.
func TestNativeDockerPullEndsWhenItsRegistryStalls(t *testing.T) {
	f := newNativePullFixture(t)
	const window = time.Second
	t.Run("stalled", func(t *testing.T) {
		image, stalled, ended := f.stallingImage(8<<20, 3<<20)
		// The floor is a step of the layer each window, as containerd writes it, so the registry
		// is ended in its second window without progress.
		engine := &Engine{Docker: f.docker, registryProgress: &registryProgress{window: window, least: 1 << 20}}
		done := make(chan error, 1)
		go func() { done <- engine.pullImage(f.ctx, image) }()
		var began time.Time
		select {
		case <-stalled:
			began = time.Now()
		case err := <-done:
			t.Fatal("the pull ended before the registry stalled", err)
		case <-time.After(time.Minute):
			t.Fatal("Docker did not read the layer")
		}
		select {
		case err := <-done:
			if !errors.Is(err, errDockerPullStalled) {
				t.Fatal(err)
			}
			// Within its third window, with time for the pull to stop.
			if waited := time.Since(began); waited > 3*window+time.Second {
				t.Fatal("stopped after", waited)
			}
		case <-time.After(time.Minute):
			t.Fatal("the stalled pull did not end")
		}
		dockerLetGo(t, f, ended, "stalled")
		if _, err := f.docker.ImageInspect(f.ctx, image); !errdefs.IsNotFound(err) {
			t.Fatal("the stalled pull left an image", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		image, stalled, ended := f.stallingImage(8<<20, 3<<20)
		journal, _ := testVisualJournal(t)
		engine := &Engine{Docker: f.docker, Journal: journal}
		command := api.ExecutionCommand{Id: uuid.New()}
		if err := journal.db.Update(func(tx *bolt.Tx) error {
			return journal.put(tx, "operations", command.Id.String(), Record{Command: command})
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			pulling, stop := engine.pullContext(f.ctx, command, time.Hour)
			defer stop()
			done <- engine.pullImage(pulling, image)
		}()
		select {
		case <-stalled:
		case err := <-done:
			t.Fatal("the pull ended before the registry stalled", err)
		case <-time.After(time.Minute):
			t.Fatal("Docker did not read the layer")
		}
		// Long enough that Docker waits on the registry, short of the floor's first window.
		time.Sleep(2 * time.Second)
		if err := journal.Mutate(command.Id, func(r *Record) { r.Command.CancelRequested = true }); err != nil {
			t.Fatal(err)
		}
		cancelled := time.Now()
		select {
		case err := <-done:
			if !errors.Is(err, errPullCancelled) {
				t.Fatal(err)
			}
			if waited := time.Since(cancelled); waited > time.Second {
				t.Fatal("cancelled after", waited)
			}
		case <-time.After(time.Minute):
			t.Fatal("the cancelled pull did not end")
		}
		dockerLetGo(t, f, ended, "cancelled")
		if _, err := f.docker.ImageInspect(f.ctx, image); !errdefs.IsNotFound(err) {
			t.Fatal("the cancelled pull left an image", err)
		}
	})
}

// dockerLetGo checks that Docker let go of the registry once the agent ended its pull. Docker's
// classic image store does not: it keeps reading the layer, holding the connection open, until the
// registry ends it, though it reads the layer afresh for a pull made again.
func dockerLetGo(t *testing.T, f *nativePullFixture, ended <-chan struct{}, how string) {
	t.Helper()
	info, err := f.docker.Info(f.ctx, client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !containerdImageStore(info.Info.DriverStatus) {
		t.Log("Docker's classic image store keeps reading from the registry after the agent ended the pull")
		return
	}
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("Docker kept reading from the registry after the agent ended the", how, "pull")
	}
}
