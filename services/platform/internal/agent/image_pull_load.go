package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"hash"
	"io"
	"time"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// loadRegistryImage puts the image plan describes into Docker's containerd image store by
// loading it, so the agent is the image's only reader from its registry. The agent reads each
// blob once, at most its descriptor's size, and passes it to Docker as an OCI archive while it
// measures it as a local build's load is measured (image_archive.go). Docker imports nothing
// before the archive ends, and the agent ends it only once the whole image is within limit and
// every blob matches its digest. The image is named by the reference the deployment pulls, so
// it resolves, and has the same ID, as if Docker had pulled it.
func (e *Engine) loadRegistryImage(ctx context.Context, named reference.Canonical, source registryImage, plan *registryPlan, limit imageLoadLimit) error {
	reader, writer := io.Pipe()
	streamed := make(chan error, 1)
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		err := streamRegistryArchive(streamCtx, writer, source, plan, named, limit)
		_ = writer.CloseWithError(err)
		streamed <- err
	}()
	response, loaded := e.Docker.ImageLoad(ctx, reader)
	if loaded == nil {
		loaded = imageLoadOutcome(response)
		_ = response.Close()
	}
	_ = reader.CloseWithError(errors.New("Docker stopped reading the image archive"))
	var checked error
	select {
	case checked = <-streamed:
	case <-time.After(5 * time.Second):
		// Docker has stopped reading, and a registry that stopped sending would hold the stream.
		stop()
		checked = <-streamed
	}
	var refused *imageLoadRefused
	var unread *imageArchiveUnread
	switch {
	case errors.As(checked, &refused):
		return refused
	case errors.As(checked, &unread):
		return registryUnreachable(unread.err)
	case loaded != nil:
		return &imageLoadRefused{why: "Docker's image load failed: " + firstLine(loaded.Error(), 300)}
	case checked != nil:
		return &imageLoadRefused{why: "Docker stopped reading it"}
	}
	image, err := e.Docker.ImageInspect(ctx, named.String())
	if err != nil || image.ID != plan.documents[0].desc.Digest.String() {
		return &imageLoadRefused{why: "Docker did not load it under the name the deployment uses"}
	}
	return nil
}

// errorWriter records the first error writing to w.
type errorWriter struct {
	w   io.Writer
	err error
}

func (e *errorWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

// streamRegistryArchive writes the image plan describes to out as an OCI archive: its layout,
// its documents and config, each distinct layer as read from its registry, then index.json,
// which names the image, and the archive's end. A member's header reaches Docker only after
// plan counted the member's size, and the archive's end only after the whole image is
// measured within limit. It returns nil once the end is written; on an error the caller must
// fail out instead, so Docker never imports the image.
func streamRegistryArchive(ctx context.Context, out io.Writer, source registryImage, plan *registryPlan, named reference.Canonical, limit imageLoadLimit) error {
	docker := &errorWriter{w: out}
	tw := tar.NewWriter(docker)
	failed := func(err error) error {
		if docker.err != nil {
			return &imageLoadRefused{why: "Docker stopped reading it"}
		}
		return err
	}
	member := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0444, Size: int64(len(data))}); err != nil {
			return failed(err)
		}
		_, err := tw.Write(data)
		return failed(err)
	}
	if err := member(ocispec.ImageLayoutFile, []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}
	for _, document := range append(append([]registryDocument{}, plan.documents...), plan.config) {
		if err := member("blobs/sha256/"+document.desc.Digest.Encoded(), document.data); err != nil {
			return err
		}
	}
	running := plan.stored
	unpacked := map[string]imageCost{}
	for _, layer := range plan.layers {
		if _, done := unpacked[layer.Digest.String()]; done {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + layer.Digest.Encoded(), Typeflag: tar.TypeReg, Mode: 0444, Size: layer.Size}); err != nil {
			return failed(err)
		}
		cost, err := measureRegistryLayer(ctx, source, layer, tw, limit.less(running))
		if err != nil {
			return failed(err)
		}
		unpacked[layer.Digest.String()] = cost
		running.add(cost)
		running.add(imageCost{Bytes: storageLayerBytes, Entries: storageLayerEntries})
		if err = limit.check(running); err != nil {
			return limit.refusal(err)
		}
	}
	if _, err := registryImageCost(plan, unpacked, limit); err != nil {
		return err
	}
	root := plan.documents[0].desc
	// containerd names the image it imports by this annotation, as a pull by this reference does.
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ocispec.MediaTypeImageIndex,
		"manifests":     []ocispec.Descriptor{{MediaType: root.MediaType, Digest: root.Digest, Size: root.Size, Annotations: map[string]string{"io.containerd.image.name": named.String()}}},
	})
	if err != nil {
		return err
	}
	if err = member(ocispec.ImageIndexFile, index); err != nil {
		return err
	}
	return failed(tw.Close())
}

// measureRegistryLayer reads the layer desc names from source, at most its size, writing what it
// reads to also when that is not nil, and returns what unpacking it writes. It refuses a layer
// that differs from its digest, or that the agent cannot measure.
func measureRegistryLayer(ctx context.Context, source registryImage, desc ocispec.Descriptor, also io.Writer, limit imageLoadLimit) (imageCost, error) {
	rc, err := source.blob(ctx, desc)
	if err != nil {
		return imageCost{}, &imageArchiveUnread{err}
	}
	defer rc.Close()
	// The gate records whether reading from the registry failed; what it reads goes nowhere.
	gate := &imageArchiveGate{src: &measuredReader{r: rc, remaining: desc.Size}, out: io.Discard}
	digester := sha256.New()
	var read io.Reader = io.TeeReader(releasing{gate, gate}, digester)
	if also != nil {
		read = io.TeeReader(read, also)
	}
	member, err := inspectImageMember(read, gate, desc.Size, limit)
	if metadata := new(layerMetadataRefused); errors.As(err, &metadata) {
		return imageCost{}, metadata.note(desc.Digest.String())
	}
	if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
		return imageCost{}, limit.refusal(err)
	}
	if err != nil {
		return imageCost{}, err
	}
	if !matchesDigest(digester, desc.Digest) {
		return imageCost{}, unmeasurableImage("a layer in it differs from its digest")
	}
	return member.cost, nil
}

func matchesDigest(h hash.Hash, d digest.Digest) bool {
	return hex.EncodeToString(h.Sum(nil)) == d.Encoded()
}

// registryImageCost is what Docker writes for plan: its blobs, and each distinct chain of its
// layers unpacked, given what each layer writes.
func registryImageCost(plan *registryPlan, unpacked map[string]imageCost, limit imageLoadLimit) (imageCost, error) {
	chains, err := chainsCost(plan.chains(), func(name string) (imageCost, bool) {
		cost, ok := unpacked[name]
		return cost, ok
	}, limit.less(plan.stored))
	total := plan.stored
	total.add(chains)
	if err == nil {
		err = limit.check(total)
	}
	if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
		return total, limit.refusal(err)
	}
	return total, err
}
