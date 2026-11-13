package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"strconv"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Media types Docker pulls and the agent reads. Others, such as foreign, non-distributable or
// encrypted layers, are refused rather than measured.
var (
	pullIndexTypes    = map[string]bool{ocispec.MediaTypeImageIndex: true, "application/vnd.docker.distribution.manifest.list.v2+json": true}
	pullManifestTypes = map[string]bool{ocispec.MediaTypeImageManifest: true, "application/vnd.docker.distribution.manifest.v2+json": true}
	pullLayerTypes    = map[string]bool{ocispec.MediaTypeImageLayer: true, ocispec.MediaTypeImageLayerGzip: true, ocispec.MediaTypeImageLayerZstd: true, "application/vnd.docker.image.rootfs.diff.tar.gzip": true}
)

// An image Docker pulls has one manifest for this server, and maybe an index and attestations
// above it; this many documents is never one.
const pullManifests = 64

// imageMeasure counts what pulling one image writes.
type imageMeasure struct {
	source    registryImage
	limit     imageLoadLimit
	platforms []string
	cache     *layerCosts

	// stored is every blob Docker stores: manifests, configs and compressed layers.
	stored imageCost
	// blobs are those already counted, and metadata the bytes of documents read.
	blobs     map[digest.Digest]bool
	documents int
	metadata  int
	// chains are the layer digests of each image manifest Docker unpacks, and layers their
	// descriptors, which the image names consistently.
	chains [][]string
	layers map[digest.Digest]ocispec.Descriptor
	// unpacked is what each layer writes once unpacked.
	unpacked map[string]imageCost
	// total is what pulling the image writes, once measured.
	total imageCost
}

// image counts what pulling the image with this digest writes, and returns an error with the
// reason if that is more than the limit, or the image is one the agent cannot measure.
func (m *imageMeasure) image(ctx context.Context, root digest.Digest) error {
	m.blobs = map[digest.Digest]bool{}
	m.layers = map[digest.Digest]ocispec.Descriptor{}
	m.unpacked = map[string]imageCost{}
	if err := m.document(ctx, ocispec.Descriptor{Digest: root, Size: -1}, 0, true); err != nil {
		return err
	}
	if len(m.chains) == 0 {
		return unmeasurableImage("it has no image for this server's platform")
	}
	// Each layer is unpacked at least once, so a layer that alone takes the image past its
	// limit is refused before the next is read.
	running := m.stored
	for _, chain := range m.chains {
		for _, name := range chain {
			if _, done := m.unpacked[name]; done {
				continue
			}
			cost, err := m.layer(ctx, m.layers[digest.Digest(name)], m.limit.less(running))
			if err != nil {
				return err
			}
			m.unpacked[name] = cost
			running.add(cost)
			running.add(imageCost{Bytes: storageLayerBytes, Entries: storageLayerEntries})
			if err = m.limit.check(running); err != nil {
				return m.limit.refusal(err)
			}
		}
	}
	unpacked, err := chainsCost(m.chains, func(name string) (imageCost, bool) {
		cost, ok := m.unpacked[name]
		return cost, ok
	}, m.limit.less(m.stored))
	if err == nil {
		m.total = m.stored
		m.total.add(unpacked)
		err = m.limit.check(m.total)
	}
	if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
		return m.limit.refusal(err)
	}
	return err
}

// store counts one blob Docker stores, once however many documents name it.
func (m *imageMeasure) store(desc ocispec.Descriptor) error {
	if desc.Digest.Algorithm() != digest.SHA256 || desc.Digest.Validate() != nil || desc.Size < 0 {
		return unmeasurableImage("it names content by something other than a sha256 digest and size")
	}
	if len(desc.URLs) > 0 {
		return unmeasurableImage("it names content Docker would download from elsewhere")
	}
	if m.blobs[desc.Digest] {
		return nil
	}
	m.blobs[desc.Digest] = true
	m.stored.add(entryCost(&tar.Header{Name: "blobs/sha256/" + desc.Digest.Encoded(), Typeflag: tar.TypeReg, Size: desc.Size}))
	if err := m.limit.check(m.stored); err != nil {
		return m.limit.refusal(err)
	}
	return nil
}

// document reads the index or manifest desc names and counts the blobs Docker stores for it:
// for an index, those of each manifest for this server's platform and their attestations; for
// a manifest, its config and layers, which it records to measure. An attestation is stored but
// never unpacked.
func (m *imageMeasure) document(ctx context.Context, desc ocispec.Descriptor, depth int, unpack bool) error {
	if depth > imageIndexDepth {
		return unmeasurableImage("its image index is nested too deeply")
	}
	if m.documents++; m.documents > pullManifests {
		return unmeasurableImage("it has more manifests than an image needs")
	}
	data, mediaType, err := m.source.manifest(ctx, desc.Digest, imageMetadataBytes)
	if err != nil {
		return &imageArchiveUnread{err}
	}
	if len(data) > imageMetadataBytes || desc.Size >= 0 && int64(len(data)) != desc.Size {
		return unmeasurableImage("a manifest in it differs from its descriptor")
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != desc.Digest.Encoded() {
		return unmeasurableImage("a manifest in it differs from its digest")
	}
	if m.metadata += len(data); m.metadata > imageMetadataTotal {
		return unmeasurableImage("it has more metadata than an image needs")
	}
	desc.Size = int64(len(data))
	// Docker's legacy store reads a manifest by the type its registry gives, and containerd by
	// the type its descriptor gives, so the two must agree.
	if desc.MediaType == "" {
		desc.MediaType = mediaType
	} else if mediaType != "" && mediaType != desc.MediaType {
		return unmeasurableImage("its registry gives a manifest a media type other than its index does")
	}
	if err = m.store(desc); err != nil {
		return err
	}
	// Docker reads a document by its media type, and the agent requires the document to agree.
	var declared string
	if value, err := foldedMember(data, "mediaType"); err != nil {
		return err
	} else if value != nil && (json.Unmarshal(value, &declared) != nil || declared != "" && declared != desc.MediaType) {
		return unmeasurableImage("a manifest in it declares a media type its registry does not")
	}
	manifests, err := foldedMember(data, "manifests")
	if err != nil {
		return err
	}
	config, err := foldedMember(data, "config")
	if err != nil {
		return err
	}
	layers, err := foldedMember(data, "layers")
	if err != nil {
		return err
	}
	switch {
	case pullIndexTypes[desc.MediaType] && config == nil && layers == nil:
		children, err := descriptors(manifests)
		if err != nil {
			return err
		}
		selected := map[digest.Digest]bool{}
		for _, child := range children {
			if m.forPlatform(child) {
				selected[child.Digest] = true
			}
		}
		for _, child := range children {
			attestation := child.Annotations["vnd.docker.reference.type"] == "attestation-manifest" && selected[digest.Digest(child.Annotations["vnd.docker.reference.digest"])]
			if !selected[child.Digest] && !attestation {
				continue
			}
			if !pullIndexTypes[child.MediaType] && !pullManifestTypes[child.MediaType] {
				return &imageLoadRefused{why: "its image index names a document of type " + strconv.Quote(truncated(child.MediaType, 100)) + ", which Docker does not pull"}
			}
			if err = m.document(ctx, child, depth+1, unpack && !attestation); err != nil {
				return err
			}
		}
		return nil
	case pullManifestTypes[desc.MediaType] && manifests == nil && config != nil:
		configDescriptor, err := descriptor(config)
		if err != nil {
			return err
		}
		if err = m.store(configDescriptor); err != nil {
			return err
		}
		layerDescriptors, err := descriptors(layers)
		if err != nil {
			return err
		}
		if len(layerDescriptors) > imageChainDepth {
			return unmeasurableImage("it has an image with more layers than Docker can stack")
		}
		chain := make([]string, 0, len(layerDescriptors))
		for _, layer := range layerDescriptors {
			if unpack && !pullLayerTypes[layer.MediaType] {
				return &imageLoadRefused{why: "it has a layer of type " + strconv.Quote(truncated(layer.MediaType, 100)) + ", which the agent does not pull"}
			}
			if err = m.store(layer); err != nil {
				return err
			}
			if previous, ok := m.layers[layer.Digest]; ok && previous.Size != layer.Size {
				return unmeasurableImage("it gives one layer two sizes")
			}
			m.layers[layer.Digest] = layer
			chain = append(chain, layer.Digest.String())
		}
		if unpack {
			m.chains = append(m.chains, chain)
		}
		return nil
	}
	return unmeasurableImage("it has a manifest of a kind Docker does not pull, or one that is not valid")
}

// foldedMember is the value of an object's member named name, ignoring case as Docker's JSON
// decoding does, or nil if it has none or it is null. An object with two such members is
// refused, since which one Docker reads depends on their order.
func foldedMember(raw []byte, name string) (jsontext.Value, error) {
	values, err := foldedMembers(raw, name)
	switch {
	case err != nil:
		return nil, err
	case len(values) > 1:
		return nil, unmeasurableImage("its image metadata names one field twice")
	case len(values) == 1:
		return values[0], nil
	}
	return nil, nil
}

// descriptors reads a list of OCI descriptors, which may be absent.
func descriptors(raw jsontext.Value) ([]ocispec.Descriptor, error) {
	if raw == nil {
		return nil, nil
	}
	var list []jsontext.Value
	if json.Unmarshal(raw, &list) != nil {
		return nil, unmeasurableImage("its image metadata is not valid")
	}
	out := make([]ocispec.Descriptor, 0, len(list))
	for _, item := range list {
		d, err := descriptor(item)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// descriptor reads the fields of an OCI descriptor that decide what Docker pulls, each by the
// name Docker matches it with.
func descriptor(raw jsontext.Value) (ocispec.Descriptor, error) {
	var d ocispec.Descriptor
	invalid := unmeasurableImage("its image metadata is not valid")
	fields := map[string]any{"mediaType": &d.MediaType, "digest": &d.Digest, "size": &d.Size, "urls": &d.URLs, "annotations": &d.Annotations}
	for name, out := range fields {
		value, err := foldedMember(raw, name)
		if err != nil {
			return d, err
		}
		if value != nil && json.Unmarshal(value, out) != nil {
			return d, invalid
		}
	}
	platform, err := foldedMember(raw, "platform")
	if err != nil || platform == nil {
		return d, err
	}
	d.Platform = &ocispec.Platform{}
	for name, out := range map[string]*string{"os": &d.Platform.OS, "architecture": &d.Platform.Architecture} {
		value, err := foldedMember(platform, name)
		if err != nil {
			return d, err
		}
		if value != nil && json.Unmarshal(value, out) != nil {
			return d, invalid
		}
	}
	return d, nil
}

// forPlatform reports whether Docker pulls an index's entry on this server: one for its
// platform, in any variant, or one that names no platform.
func (m *imageMeasure) forPlatform(desc ocispec.Descriptor) bool {
	if desc.Platform == nil {
		return true
	}
	if desc.Platform.OS != "linux" {
		return false
	}
	for _, arch := range m.platforms {
		if desc.Platform.Architecture == arch {
			return true
		}
	}
	return false
}

// layer is what unpacking the layer desc names writes. It reads the layer from the registry
// and measures it as a local build's layer is measured, unless it measured it before.
func (m *imageMeasure) layer(ctx context.Context, desc ocispec.Descriptor, limit imageLoadLimit) (imageCost, error) {
	if cost, ok := m.cache.get(desc.Digest); ok {
		return cost, nil
	}
	rc, err := m.source.blob(ctx, desc)
	if err != nil {
		return imageCost{}, &imageArchiveUnread{err}
	}
	defer rc.Close()
	// The layer reaches no one: the gate only records whether reading it failed.
	gate := &imageArchiveGate{src: &measuredReader{r: rc, remaining: desc.Size}, out: io.Discard}
	digester := sha256.New()
	member, err := inspectImageMember(io.TeeReader(releasing{gate, gate}, digester), gate, desc.Size, limit)
	if metadata := new(layerMetadataRefused); errors.As(err, &metadata) {
		return imageCost{}, metadata.note(desc.Digest.String())
	}
	if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
		return imageCost{}, limit.refusal(err)
	}
	if err != nil {
		return imageCost{}, err
	}
	if hex.EncodeToString(digester.Sum(nil)) != desc.Digest.Encoded() {
		return imageCost{}, unmeasurableImage("a layer in it differs from its digest")
	}
	m.cache.put(desc.Digest, member.cost)
	return member.cost, nil
}
