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
	"slices"
	"strconv"
	"sync"

	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
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
	// attached are the blobs of the chosen manifest's attestations, which Docker stores but never
	// unpacks; their manifests are among documents.
	attached []ocispec.Descriptor
}

// imageAttestations is the most attestations one image may have; BuildKit pushes one or two.
// imageIndexEntries is the most entries an image's index may have: an image and an attestation
// for each of a few dozen platforms.
const (
	imageAttestations = 16
	imageIndexEntries = 64
)

// leafBlobs refuses a manifest whose config or layers name a manifest or index. Docker's load
// walks every document below the image's index whose content it holds, and unpacks those it
// finds for the server's platform; with this, the only such documents are the index's own
// entries, which choosePlatform checks.
func leafBlobs(blobs ...ocispec.Descriptor) error {
	for _, blob := range blobs {
		if pullManifestTypes[blob.MediaType] || pullIndexTypes[blob.MediaType] {
			return unmeasurableImage("it has a manifest whose config or layers name another manifest or index")
		}
	}
	return nil
}

// checkSiblings reads each entry of the image's index other than those plan holds, and refuses
// one that names a manifest or index below it (leafBlobs). Docker never pulls these, but its
// load walks them when it holds their content from elsewhere.
func checkSiblings(ctx context.Context, source registryImage, children []ocispec.Descriptor, plan *registryPlan, checked *checkedManifests) error {
	for _, child := range children {
		if checked.has(child.Digest) || slices.ContainsFunc(plan.documents, func(d registryDocument) bool { return d.desc.Digest == child.Digest }) {
			continue
		}
		document, kind, err := fetchDocument(ctx, source, child)
		if missing(err) {
			return unmeasurableImage("its index names the manifest " + truncated(child.Digest.String(), 19) + ", which its registry no longer has, so the agent cannot check what Docker would do with it")
		}
		if err != nil {
			return err
		}
		if kind != "manifest" {
			return unmeasurableImage("its image index names an index as a manifest")
		}
		config, err := foldedMember(document.data, "config")
		if err != nil {
			return err
		}
		layers, err := foldedMember(document.data, "layers")
		if err != nil {
			return err
		}
		configDescriptor, err := descriptor(config)
		if err != nil {
			return err
		}
		layerDescriptors, err := descriptors(layers)
		if err != nil {
			return err
		}
		if err = leafBlobs(append(layerDescriptors, configDescriptor)...); err != nil {
			return err
		}
		checked.add(child.Digest)
	}
	return nil
}

// missing reports whether err is the registry saying it has no such content.
func missing(err error) bool {
	var unread *imageArchiveUnread
	return errors.As(err, &unread) && errors.Is(unread.err, errdef.ErrNotFound)
}

// checkedManifests are manifests checkSiblings found harmless, by digest, so a redeploy does
// not read them from the registry again.
type checkedManifests struct {
	mu      sync.Mutex
	digests map[digest.Digest]bool
}

func (c *checkedManifests) has(d digest.Digest) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.digests[d]
}

func (c *checkedManifests) add(d digest.Digest) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.digests == nil || len(c.digests) >= layerCostEntries {
		c.digests = map[digest.Digest]bool{}
	}
	c.digests[d] = true
}

// planAttestations adds the attestations of chosen in the index to plan. Docker's load of one
// platform reads them as the chosen manifest's referrers and fails without them, but stores
// them without unpacking, as it skips any image marked as one.
func planAttestations(ctx context.Context, source registryImage, children []ocispec.Descriptor, chosen ocispec.Descriptor, plan *registryPlan, store func(ocispec.Descriptor) error) error {
	attestations := 0
	attached := map[digest.Digest]bool{}
	for _, child := range children {
		if child.Annotations["vnd.docker.reference.digest"] != chosen.Digest.String() || child.Digest == chosen.Digest {
			continue
		}
		if attestations++; attestations > imageAttestations {
			return unmeasurableImage("its image has more attestations than an image needs")
		}
		if !pullManifestTypes[child.MediaType] {
			return &imageLoadRefused{why: "its image has an attestation of the media type " + strconv.Quote(truncated(child.MediaType, 100)) + ", which Docker does not pull"}
		}
		document, kind, err := fetchDocument(ctx, source, child)
		if missing(err) {
			return unmeasurableImage("its image's attestation " + truncated(child.Digest.String(), 19) + " is no longer in its registry, and Docker's load needs it")
		}
		if err != nil {
			return err
		}
		if kind != "manifest" {
			return unmeasurableImage("its image has an attestation that is not a manifest")
		}
		if err = store(document.desc); err != nil {
			return err
		}
		plan.documents = append(plan.documents, document)
		config, err := foldedMember(document.data, "config")
		if err != nil {
			return err
		}
		layers, err := foldedMember(document.data, "layers")
		if err != nil {
			return err
		}
		blobs, err := descriptors(layers)
		if err != nil {
			return err
		}
		if len(blobs) > imageAttestations {
			return unmeasurableImage("its image has an attestation of more statements than one needs")
		}
		configDescriptor, err := descriptor(config)
		if err != nil {
			return err
		}
		if err = leafBlobs(append(blobs, configDescriptor)...); err != nil {
			return err
		}
		for _, blob := range append(blobs, configDescriptor) {
			if err = store(blob); err != nil {
				return err
			}
			if !attached[blob.Digest] {
				attached[blob.Digest] = true
				plan.attached = append(plan.attached, blob)
			}
		}
	}
	return nil
}

// planRegistryImage reads the image with digest root from source and chooses its manifest for
// platform as Docker does. It refuses images the agent cannot measure, and images whose blobs
// alone are more than limit.
//
// When loading, the image goes into Docker's containerd store with its index, so the plan also
// holds the chosen image's attestations, which Docker's load reads, and checks the index's
// other entries (checkSiblings), whose verdicts checked keeps.
func planRegistryImage(ctx context.Context, source registryImage, root digest.Digest, platform ocispec.Platform, limit imageLoadLimit, loading bool, checked *checkedManifests) (*registryPlan, error) {
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
		if loading {
			if err = planAttestations(ctx, source, children, chosen, plan, store); err != nil {
				return nil, err
			}
			if err = checkSiblings(ctx, source, children, plan, checked); err != nil {
				return nil, err
			}
		}
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
	// The config says which platform the image is for. Docker would pull a single-platform image
	// for another architecture, to run under emulation, but the agent refuses it.
	data, err := fetchBlob(ctx, source, configDescriptor)
	if err != nil {
		return nil, err
	}
	plan.config = registryDocument{configDescriptor, data}
	// Each blob is in the archive once, as what one document names it, so the image's documents,
	// config, layers and attestations' blobs are refused if any two of them share a digest.
	kinds := map[digest.Digest]string{}
	for kind, list := range map[string][]ocispec.Descriptor{"document": documentDescriptors(plan.documents, plan.config), "layer": plan.layers, "attached": plan.attached} {
		for _, desc := range list {
			if other, ok := kinds[desc.Digest]; ok && other != kind {
				return nil, unmeasurableImage("it uses one blob in two roles, such as a layer and a config")
			}
			kinds[desc.Digest] = kind
		}
	}
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

func documentDescriptors(documents []registryDocument, config registryDocument) []ocispec.Descriptor {
	out := []ocispec.Descriptor{config.desc}
	for _, document := range documents {
		out = append(out, document.desc)
	}
	return out
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
	if len(children) > imageIndexEntries {
		return ocispec.Descriptor{}, unmeasurableImage("its image index has more entries than an image needs")
	}
	var chosen *ocispec.Descriptor
	for _, child := range children {
		if !pullManifestTypes[child.MediaType] {
			// Docker walks every index below the image's, and unpacks what it finds there.
			return ocispec.Descriptor{}, &imageLoadRefused{why: "its image index names a document of the media type " + strconv.Quote(truncated(child.MediaType, 100)) + ", which the agent does not pull"}
		}
	}
	for i, child := range children {
		_, attestation := child.Annotations["vnd.docker.reference.type"]
		switch {
		case child.Platform == nil:
			// Docker's load of one platform reads such an entry whatever it is.
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
		case p.OSVersion != "" || len(p.OSFeatures) > 0:
			// Docker matches an image that needs OS features or a version to no Linux server.
			return ocispec.Descriptor{}, unmeasurableImage("its image for this server's architecture names an OS version or features")
		}
		chosen = &children[i]
	}
	if chosen == nil {
		return ocispec.Descriptor{}, unmeasurableImage("it has no image for this server's platform, " + host.OS + "/" + host.Architecture)
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
	for name, out := range map[string]any{"os": &d.Platform.OS, "architecture": &d.Platform.Architecture, "variant": &d.Platform.Variant, "os.version": &d.Platform.OSVersion, "os.features": &d.Platform.OSFeatures} {
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
