package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"strconv"

	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Media types Docker pulls and the agent reads. Others are refused rather than measured.
var (
	pullIndexTypes    = map[string]bool{ocispec.MediaTypeImageIndex: true, "application/vnd.docker.distribution.manifest.list.v2+json": true}
	pullManifestTypes = map[string]bool{ocispec.MediaTypeImageManifest: true, "application/vnd.docker.distribution.manifest.v2+json": true}
	pullConfigTypes   = map[string]bool{ocispec.MediaTypeImageConfig: true, "application/vnd.docker.container.image.v1+json": true}
	pullLayerTypes    = map[string]bool{ocispec.MediaTypeImageLayer: true, ocispec.MediaTypeImageLayerGzip: true, ocispec.MediaTypeImageLayerZstd: true, "application/vnd.docker.image.rootfs.diff.tar.gzip": true}
	// Layers Docker would download from the URLs their descriptors name rather than from the
	// image's registry.
	pullForeignTypes = map[string]bool{ocispec.MediaTypeImageLayerNonDistributable: true, ocispec.MediaTypeImageLayerNonDistributableGzip: true, ocispec.MediaTypeImageLayerNonDistributableZstd: true, "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip": true, "application/vnd.docker.image.rootfs.foreign.diff.tar": true}
)

// registryDocument is a manifest or index, verified against its digest.
type registryDocument struct {
	desc ocispec.Descriptor
	data []byte
}

// registryPlan is what Docker stores to pull one image onto this server: the image's index, if
// it has one, the manifest for this server's platform, its config and its layers.
type registryPlan struct {
	documents []registryDocument
	config    registryDocument
	// layers are the manifest's layers in order, a layer listed twice included.
	layers []ocispec.Descriptor
	// stored is each distinct blob as Docker stores it.
	stored imageCost
	// platform is the chosen manifest's platform in the image's index, normalized, or nil when
	// the image has no index.
	platform *ocispec.Platform
}

// planRegistryImage reads the image with digest root from source and chooses its manifest for
// platform as Docker does. It refuses images the agent cannot measure, and images whose blobs
// alone are more than limit.
func planRegistryImage(ctx context.Context, source registryImage, root digest.Digest, platform ocispec.Platform, limit imageLoadLimit) (*registryPlan, error) {
	plan := &registryPlan{}
	seen := map[digest.Digest]bool{}
	store := func(desc ocispec.Descriptor) error {
		if desc.Digest.Algorithm() != digest.SHA256 || desc.Digest.Validate() != nil || desc.Size < 0 {
			return unmeasurableImage("it names content by something other than a sha256 digest and size")
		}
		if len(desc.URLs) > 0 {
			return &imageLoadRefused{why: "it names content Docker would download from other URLs than its registry"}
		}
		if seen[desc.Digest] {
			return nil
		}
		seen[desc.Digest] = true
		plan.stored.add(entryCost(&tar.Header{Name: "blobs/sha256/" + desc.Digest.Encoded(), Typeflag: tar.TypeReg, Size: desc.Size}))
		if err := limit.check(plan.stored); err != nil {
			return limit.refusal(err)
		}
		return nil
	}
	document, kind, err := fetchDocument(ctx, source, ocispec.Descriptor{Digest: root, Size: -1})
	if err != nil {
		return nil, err
	}
	if err = store(document.desc); err != nil {
		return nil, err
	}
	plan.documents = append(plan.documents, document)
	if kind == "index" {
		manifests, err := foldedMember(document.data, "manifests")
		if err != nil {
			return nil, err
		}
		children, err := descriptors(manifests)
		if err != nil {
			return nil, err
		}
		chosen, err := choosePlatform(children, platform)
		if err != nil {
			return nil, err
		}
		normalized := platforms.Normalize(*chosen.Platform)
		plan.platform = &normalized
		if document, kind, err = fetchDocument(ctx, source, chosen); err != nil {
			return nil, err
		}
		if kind != "manifest" {
			return nil, unmeasurableImage("its image index names another index for this server's platform")
		}
		if err = store(document.desc); err != nil {
			return nil, err
		}
		plan.documents = append(plan.documents, document)
	}
	config, err := foldedMember(document.data, "config")
	if err != nil {
		return nil, err
	}
	layers, err := foldedMember(document.data, "layers")
	if err != nil {
		return nil, err
	}
	configDescriptor, err := descriptor(config)
	if err != nil {
		return nil, err
	}
	if !pullConfigTypes[configDescriptor.MediaType] {
		return nil, &imageLoadRefused{why: "its config has the media type " + strconv.Quote(truncated(configDescriptor.MediaType, 100)) + ", which is not an image's"}
	}
	if configDescriptor.Size > imageMetadataBytes {
		return nil, unmeasurableImage("its config is larger than an image needs")
	}
	if err = store(configDescriptor); err != nil {
		return nil, err
	}
	if plan.layers, err = descriptors(layers); err != nil {
		return nil, err
	}
	if len(plan.layers) > imageChainDepth {
		return nil, unmeasurableImage("it has more layers than Docker can stack")
	}
	sizes := map[digest.Digest]int64{}
	for _, layer := range plan.layers {
		switch {
		case pullForeignTypes[layer.MediaType]:
			return nil, &imageLoadRefused{why: "it has a foreign, non-distributable layer of type " + strconv.Quote(layer.MediaType) + ", which Docker would download from elsewhere than its registry"}
		case !pullLayerTypes[layer.MediaType]:
			return nil, &imageLoadRefused{why: "it has a layer of the media type " + strconv.Quote(truncated(layer.MediaType, 100)) + ", which the agent does not pull"}
		}
		if size, ok := sizes[layer.Digest]; ok && size != layer.Size {
			return nil, unmeasurableImage("it gives one layer two sizes")
		}
		sizes[layer.Digest] = layer.Size
		if err = store(layer); err != nil {
			return nil, err
		}
	}
	// The config says which platform the image is for, and an image for another would not run.
	data, err := fetchBlob(ctx, source, configDescriptor)
	if err != nil {
		return nil, err
	}
	plan.config = registryDocument{configDescriptor, data}
	var configPlatform ocispec.Platform
	for name, out := range map[string]*string{"os": &configPlatform.OS, "architecture": &configPlatform.Architecture, "variant": &configPlatform.Variant} {
		value, err := foldedMember(data, name)
		if err != nil {
			return nil, err
		}
		if value != nil && json.Unmarshal(value, out) != nil {
			return nil, unmeasurableImage("its config is not valid")
		}
	}
	if !platformMatches(configPlatform, platform) {
		return nil, unmeasurableImage("its image for this server's platform is built for " + strconv.Quote(truncated(configPlatform.OS+"/"+configPlatform.Architecture, 64)))
	}
	return plan, nil
}

// chains is the image's one chain of layers, by digest.
func (p *registryPlan) chains() [][]string {
	chain := make([]string, len(p.layers))
	for i, layer := range p.layers {
		chain[i] = layer.Digest.String()
	}
	return [][]string{chain}
}

// fetchDocument reads the manifest or index desc names, of at most imageMetadataBytes, and
// says which it is by the media type its registry and its descriptor give, which must agree.
func fetchDocument(ctx context.Context, source registryImage, desc ocispec.Descriptor) (registryDocument, string, error) {
	data, mediaType, err := source.manifest(ctx, desc.Digest, imageMetadataBytes)
	if err != nil {
		return registryDocument{}, "", &imageArchiveUnread{err}
	}
	if len(data) > imageMetadataBytes || desc.Size >= 0 && int64(len(data)) != desc.Size {
		return registryDocument{}, "", unmeasurableImage("a manifest in it differs from its descriptor")
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != desc.Digest.Encoded() {
		return registryDocument{}, "", unmeasurableImage("a manifest in it differs from its digest")
	}
	desc.Size = int64(len(data))
	// Docker's legacy store reads a manifest by the type its registry gives, and containerd by
	// the type its descriptor gives, so the two must agree.
	if desc.MediaType == "" {
		desc.MediaType = mediaType
	} else if mediaType != "" && mediaType != desc.MediaType {
		return registryDocument{}, "", unmeasurableImage("its registry gives a manifest a media type other than its index does")
	}
	var declared string
	if value, err := foldedMember(data, "mediaType"); err != nil {
		return registryDocument{}, "", err
	} else if value != nil && (json.Unmarshal(value, &declared) != nil || declared != "" && declared != desc.MediaType) {
		return registryDocument{}, "", unmeasurableImage("a manifest in it declares a media type its registry does not")
	}
	manifests, err := foldedMember(data, "manifests")
	if err != nil {
		return registryDocument{}, "", err
	}
	config, err := foldedMember(data, "config")
	if err != nil {
		return registryDocument{}, "", err
	}
	layers, err := foldedMember(data, "layers")
	if err != nil {
		return registryDocument{}, "", err
	}
	document := registryDocument{desc, data}
	switch {
	case pullIndexTypes[desc.MediaType] && config == nil && layers == nil:
		return document, "index", nil
	case pullManifestTypes[desc.MediaType] && manifests == nil && config != nil:
		return document, "manifest", nil
	case !pullIndexTypes[desc.MediaType] && !pullManifestTypes[desc.MediaType]:
		return registryDocument{}, "", &imageLoadRefused{why: "it has a manifest of the media type " + strconv.Quote(truncated(desc.MediaType, 100)) + ", which Docker does not pull"}
	}
	return registryDocument{}, "", unmeasurableImage("it has a manifest that is not valid for its media type")
}

// fetchBlob reads a small blob whole, at most its descriptor's size, and checks its digest.
func fetchBlob(ctx context.Context, source registryImage, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := source.blob(ctx, desc)
	if err != nil {
		return nil, &imageArchiveUnread{err}
	}
	defer rc.Close()
	data, err := io.ReadAll(&measuredReader{r: rc, remaining: desc.Size})
	if err != nil {
		return nil, &imageArchiveUnread{err}
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != desc.Digest.Encoded() {
		return nil, unmeasurableImage("a blob in it differs from its digest")
	}
	return data, nil
}

// choosePlatform chooses an index's manifest for platform as Docker does, with containerd's
// platform normalization. Docker prefers an image for the server's own architecture to one for
// another it can run, so it chooses the index's only image for the server's architecture, in
// the variant every server of it runs; an index with no such image, or more than one, is
// refused. Docker skips attestations and images for unknown platforms, and the agent refuses an
// entry with no platform, as which image Docker chooses then depends on its config.
func choosePlatform(children []ocispec.Descriptor, platform ocispec.Platform) (ocispec.Descriptor, error) {
	host := platforms.Normalize(platform)
	var chosen *ocispec.Descriptor
	for i, child := range children {
		_, attestation := child.Annotations["vnd.docker.reference.type"]
		switch {
		case child.Platform == nil && attestation:
			continue
		case child.Platform == nil:
			return ocispec.Descriptor{}, unmeasurableImage("its image index has a manifest without a platform")
		case child.Platform.OS == "unknown" && child.Platform.Architecture == "unknown":
			continue
		}
		p := platforms.Normalize(*child.Platform)
		if p.OS != host.OS || p.Architecture != host.Architecture {
			continue
		}
		switch {
		case chosen != nil:
			return ocispec.Descriptor{}, unmeasurableImage("its image index has more than one image for this server's architecture")
		case attestation:
			// Docker's classic store pulls by platform alone, so it would choose this.
			return ocispec.Descriptor{}, unmeasurableImage("its image index marks its image for this server's architecture as an attestation")
		case p.Variant != "":
			return ocispec.Descriptor{}, unmeasurableImage("its image for this server's architecture is for the variant " + strconv.Quote(truncated(p.Variant, 16)) + ", which not every such server runs")
		}
		chosen = &children[i]
	}
	if chosen == nil {
		return ocispec.Descriptor{}, unmeasurableImage("it has no image for this server's platform, " + host.OS + "/" + host.Architecture)
	}
	if !pullManifestTypes[chosen.MediaType] && !pullIndexTypes[chosen.MediaType] {
		return ocispec.Descriptor{}, &imageLoadRefused{why: "its image index names a document of the media type " + strconv.Quote(truncated(chosen.MediaType, 100)) + " for this server, which Docker does not pull"}
	}
	return *chosen, nil
}

// platformMatches reports whether an image for p runs on a server of platform host: the same
// OS and architecture as containerd normalizes them, in the variant every such server runs.
func platformMatches(p, host ocispec.Platform) bool {
	p, host = platforms.Normalize(p), platforms.Normalize(host)
	return p.OS == host.OS && p.Architecture == host.Architecture && p.Variant == ""
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
	if raw == nil {
		return d, invalid
	}
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
	for name, out := range map[string]*string{"os": &d.Platform.OS, "architecture": &d.Platform.Architecture, "variant": &d.Platform.Variant} {
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
