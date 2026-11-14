package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// loadRegistryImage puts the image plan describes into Docker's containerd image store by
// loading it, so the agent is the image's only reader from its registry. The agent reads each
// blob once, at most its descriptor's size, and passes it to Docker as an OCI archive while it
// measures it as a local build's load is measured (image_archive.go). containerd stores each
// blob as it arrives, all of them counted in plan, but imports and unpacks no image before the
// archive ends, and the agent ends it only once the whole image is within limit and every blob
// matches its digest. A refused load's blobs stay until containerd collects them. The image is
// named by the reference the deployment pulls, so it resolves, and has the same ID, as if
// Docker had pulled it.
func (e *Engine) loadRegistryImage(ctx context.Context, named reference.Canonical, source registryImage, plan *registryPlan, limit imageLoadLimit) error {
	// Docker unpacks every image in the loaded index for the platforms it loads, including
	// any whose content it already holds from elsewhere. So it loads only the chosen image's
	// platform, which the index has no other image for (choosePlatform).
	var options []client.ImageLoadOption
	if plan.platform != nil {
		// Docker loads one platform of an image from API 1.48, Docker 28.
		version, err := e.Docker.ServerVersion(ctx, client.ServerVersionOptions{})
		switch {
		case err != nil:
			return &imageLoadRefused{why: "the agent could not read Docker's version on this server"}
		case !apiVersionAtLeast(version.APIVersion, 1, 48):
			return &imageLoadRefused{why: "Docker on this server is older than version 28, the first that loads one platform of an image into its containerd image store", fix: "The server's owner upgrades Docker, then you {retry}."}
		}
		options = append(options, client.ImageLoadWithPlatforms(*plan.platform))
	}
	reader, writer := io.Pipe()
	streamed := make(chan error, 1)
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		err := streamRegistryArchive(streamCtx, writer, source, plan, named, limit)
		_ = writer.CloseWithError(err)
		streamed <- err
	}()
	response, loaded := e.Docker.ImageLoad(ctx, reader, options...)
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
		if checked = <-streamed; checked != nil && !errors.As(checked, new(*imageLoadRefused)) {
			checked = errDockerStopped
		}
	}
	// The stream's own failure comes first: its refusal, or the registry's. Docker's load then
	// fails too, for that reason. A stream that failed because Docker stopped reading it is
	// errDockerStopped, and Docker's own failure says why.
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

// apiVersionAtLeast reports whether the Docker API version v, such as "1.48", is at least
// major.minor.
func apiVersionAtLeast(v string, major, minor int) bool {
	a, b, ok := strings.Cut(v, ".")
	m, errA := strconv.Atoi(a)
	n, errB := strconv.Atoi(b)
	return ok && errA == nil && errB == nil && (m > major || m == major && n >= minor)
}

// errDockerStopped is Docker no longer reading the archive, for a reason its load reports.
var errDockerStopped = errors.New("Docker stopped reading the image archive")

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
			return errDockerStopped
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
	for _, blob := range plan.attached {
		if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + blob.Digest.Encoded(), Typeflag: tar.TypeReg, Mode: 0444, Size: blob.Size}); err != nil {
			return failed(err)
		}
		if err := copyRegistryBlob(ctx, source, blob, tw); err != nil {
			return failed(err)
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
	index, err := stablejson.Marshal(map[string]any{
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

// copyRegistryBlob copies the blob desc names from source to out, at most its size, and refuses
// it if it differs from its digest. Docker stores such a blob but never unpacks it.
func copyRegistryBlob(ctx context.Context, source registryImage, desc ocispec.Descriptor, out io.Writer) error {
	rc, err := source.blob(ctx, desc)
	if err != nil {
		return &imageArchiveUnread{err}
	}
	defer rc.Close()
	digester := sha256.New()
	var read io.Reader = &readFailures{&measuredReader{r: rc, remaining: desc.Size}}
	if _, err = io.Copy(io.MultiWriter(out, digester), read); err != nil {
		return err
	}
	if !matchesDigest(digester, desc.Digest) {
		return unmeasurableImage("a blob in it differs from its digest")
	}
	return nil
}

// readFailures marks a failure to read from the registry as such.
type readFailures struct{ r io.Reader }

func (r *readFailures) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		err = &imageArchiveUnread{err}
	}
	return n, err
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
	// chainsCost kept the chains within what the blobs left of limit.
	total := plan.stored
	total.add(chains)
	if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
		return total, limit.refusal(err)
	}
	return total, err
}
