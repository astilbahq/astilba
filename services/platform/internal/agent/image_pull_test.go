package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/distribution/reference"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// testRegistry serves manifests and blobs by digest, and counts what it serves.
type testRegistry struct {
	mu        sync.Mutex
	manifests map[string][2]string // digest: media type, content
	blobs     map[string][]byte
	served    map[string]int
	// overrides serve something else for a digest.
	overrides map[string][]byte
	// token, when set, is required as a bearer token, issued for basic credentials.
	username, password string
	tokens             int
}

func newTestRegistry() *testRegistry {
	return &testRegistry{manifests: map[string][2]string{}, blobs: map[string][]byte{}, served: map[string]int{}, overrides: map[string][]byte{}}
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *testRegistry) blob(data []byte) string {
	d := sha(data)
	r.blobs[d] = data
	return d
}

func (r *testRegistry) manifest(mediaType, content string) string {
	d := sha([]byte(content))
	r.manifests[d] = [2]string{mediaType, content}
	return d
}

func (r *testRegistry) count(d string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.served[d]
}

func (r *testRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		user, password, ok := req.BasicAuth()
		if !ok || user != r.username || password != r.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		r.tokens++
		r.mu.Unlock()
		fmt.Fprint(w, `{"token":"fixture-token"}`)
		return
	}
	if r.username != "" && req.Header.Get("Authorization") != "Bearer fixture-token" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="fixture",scope="repository:fixture/app:pull"`, req.Host))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/v2/fixture/app/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	r.served[parts[1]]++
	r.mu.Unlock()
	switch parts[0] {
	case "manifests":
		m, ok := r.manifests[parts[1]]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", m[0])
		w.Header().Set("Docker-Content-Digest", parts[1])
		w.Header().Set("Content-Length", fmt.Sprint(len(m[1])))
		fmt.Fprint(w, m[1])
	case "blobs":
		data, ok := r.blobs[parts[1]]
		if override, overridden := r.overrides[parts[1]]; overridden {
			data, ok = override, true
		}
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(data)
	default:
		http.NotFound(w, req)
	}
}

func descriptorJSON(mediaType, d string, size int, extra string) string {
	return fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":%d%s}`, mediaType, d, size, extra)
}

// image adds an amd64 manifest of these gzip layers and returns its digest.
func (r *testRegistry) image(layers ...[]byte) string { return r.imageFor("amd64", layers...) }

func (r *testRegistry) imageFor(arch string, layers ...[]byte) string {
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux","rootfs":{"type":"layers"}}`, arch)
	configDigest := r.blob(config)
	var descriptors []string
	for _, layer := range layers {
		descriptors = append(descriptors, descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), ""))
	}
	return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":%s,"layers":[%s]}`, ocispec.MediaTypeImageManifest, descriptorJSON(ocispec.MediaTypeImageConfig, configDigest, len(config), ""), strings.Join(descriptors, ",")))
}

func (r *testRegistry) index(entries ...string) string {
	return r.manifest(ocispec.MediaTypeImageIndex, fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[%s]}`, ocispec.MediaTypeImageIndex, strings.Join(entries, ",")))
}

func (r *testRegistry) entry(manifest, platform, annotations string) string {
	content := r.manifests[manifest][1]
	extra := ""
	if platform != "" {
		parts := append(strings.Split(platform, "/"), "")
		extra += fmt.Sprintf(`,"platform":{"os":%q,"architecture":%q,"variant":%q}`, parts[0], parts[1], parts[2])
	}
	if annotations != "" {
		extra += `,"annotations":` + annotations
	}
	return descriptorJSON(r.manifests[manifest][0], manifest, len(content), extra)
}

func (r *testRegistry) source(t *testing.T, credential *pipeline.Credential) registryImage {
	t.Helper()
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	image, err := newRegistryImage(strings.TrimPrefix(server.URL, "http://"), "fixture/app", true, true, credential, http.ProxyFromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

var pullTestLimit = imageLoadLimit{Bytes: 64 << 20, Entries: 1000, loadBytes: 8 << 30, reserveBytes: 4 << 30}

var amd64Server = ocispec.Platform{OS: "linux", Architecture: "amd64"}

// measureTest measures the image root names as a pull into Docker's classic store does.
func measureTest(t *testing.T, engine *Engine, source registryImage, root string, limit imageLoadLimit) (*registryPlan, imageCost, error) {
	t.Helper()
	plan, err := planRegistryImage(context.Background(), source, digest.Digest(root), amd64Server, limit)
	if err != nil {
		return nil, imageCost{}, err
	}
	total, err := engine.measureRegistryImage(context.Background(), source, plan, limit)
	return plan, total, err
}

// streamTest writes the archive a pull into Docker's containerd store loads.
func streamTest(t *testing.T, source registryImage, root string, limit imageLoadLimit) ([]byte, error) {
	t.Helper()
	plan, err := planRegistryImage(context.Background(), source, digest.Digest(root), amd64Server, limit)
	if err != nil {
		return nil, err
	}
	named, _ := reference.ParseNormalizedNamed("registry.example/fixture/app@" + root)
	var out bytes.Buffer
	err = streamRegistryArchive(context.Background(), &out, source, plan, named.(reference.Canonical), limit)
	return out.Bytes(), err
}

func refusalNote(t *testing.T, err error) string {
	t.Helper()
	var unread *imageArchiveUnread
	if errors.As(err, &unread) {
		err = registryUnreachable(unread.err)
	}
	var refused *imageLoadRefused
	if !errors.As(err, &refused) {
		t.Fatalf("not refused: %v", err)
	}
	return refused.pulled("registry.example/fixture/app@sha256:0")
}

// Of a multi-platform index, the agent reads only the manifest Docker chooses for this server.
func TestPullReadsOnlyThisServersImage(t *testing.T) {
	registry := newTestRegistry()
	app := gzipOf(t, tarOf(t, testFile{"app", 10 << 10}))
	other := gzipOf(t, tarOf(t, testFile{"other", 10 << 10}))
	amd64 := registry.image(app)
	arm64 := registry.imageFor("arm64", other)
	attest := func(subject string, statement []byte) string {
		return registry.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]}`, descriptorJSON(ocispec.MediaTypeImageConfig, registry.blob([]byte(`{"architecture":"unknown","os":"unknown"}`)), 41, ""), descriptorJSON("application/vnd.in-toto+json", registry.blob(statement), len(statement), "")))
	}
	ours, theirs := attest(amd64, []byte(`{"subject":"ours"}`)), attest(arm64, []byte(`{"subject":"theirs"}`))
	reference := func(subject string) string {
		return fmt.Sprintf(`{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":%q}`, subject)
	}
	root := registry.index(registry.entry(arm64, "linux/arm64/v8", ""), registry.entry(amd64, "linux/amd64", ""), registry.entry(ours, "unknown/unknown", reference(amd64)), registry.entry(theirs, "unknown/unknown", reference(arm64)))
	plan, total, err := measureTest(t, &Engine{}, registry.source(t, nil), root, pullTestLimit)
	if err != nil {
		t.Fatal(err)
	}
	// The other platform's manifests are read, to check nothing below them is an image, but
	// none of their blobs.
	if registry.count(sha(other)) != 0 || registry.count(sha([]byte(`{"subject":"theirs"}`))) != 0 || registry.count(arm64) != 1 || registry.count(theirs) != 1 {
		t.Fatal("the agent read what Docker does not pull")
	}
	if len(plan.documents) != 3 || plan.documents[1].desc.Digest.String() != amd64 || plan.documents[2].desc.Digest.String() != ours || len(plan.layers) != 1 || plan.layers[0].Digest.String() != sha(app) {
		t.Fatal("unexpected plan", plan)
	}
	// Docker reads the chosen image's attestation as its referrer, and stores but never
	// unpacks it: the index, both manifests, both configs, the statement and the layer.
	if len(plan.attached) != 2 || plan.stored.Entries != 7 || total.Entries <= plan.stored.Entries {
		t.Fatal("stored blobs", plan.attached, plan.stored, total)
	}
	archive, err := streamTest(t, registry.source(t, nil), root, pullTestLimit)
	if err != nil || !bytes.Contains(archive, []byte(`{"subject":"ours"}`)) || bytes.Contains(archive, []byte(`{"subject":"theirs"}`)) {
		t.Fatal("the loaded archive lacks the chosen image's attestation, or has another's", err)
	}
}

// The agent chooses the image Docker does, with containerd's normalization: the index's only
// image for the server's architecture. An index Docker could resolve otherwise is refused.
func TestPullChoosesThePlatformDockerDoes(t *testing.T) {
	arm64 := ocispec.Platform{OS: "linux", Architecture: "arm64"}
	plat := func(os, arch, variant string) *ocispec.Platform {
		return &ocispec.Platform{OS: os, Architecture: arch, Variant: variant}
	}
	attestation := map[string]string{"vnd.docker.reference.type": "attestation-manifest"}
	emptyOS := 1 // Normalized, an empty OS is the agent's own.
	if runtime.GOOS == "linux" {
		emptyOS = -1
	}
	for name, tc := range map[string]struct {
		host     ocispec.Platform
		children []ocispec.Descriptor
		chosen   int
	}{
		"arm64 v8":                          {arm64, []ocispec.Descriptor{{Platform: plat("linux", "amd64", "")}, {Platform: plat("linux", "arm64", "v8")}}, 1},
		"arm64 beside arm":                  {arm64, []ocispec.Descriptor{{Platform: plat("linux", "arm", "v7")}, {Platform: plat("linux", "arm64", "")}}, 1},
		"amd64 v1":                          {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "386", "")}, {Platform: plat("linux", "amd64", "v1")}}, 1},
		"x86_64 in capitals":                {amd64Server, []ocispec.Descriptor{{Platform: plat("Linux", "X86_64", "")}}, 0},
		"attestations for unknown":          {amd64Server, []ocispec.Descriptor{{Platform: plat("unknown", "unknown", ""), Annotations: attestation}, {Platform: plat("unknown", "unknown", "")}, {Platform: plat("linux", "amd64", "")}}, 2},
		"an empty OS beside linux":          {amd64Server, []ocispec.Descriptor{{Platform: plat("", "amd64", "")}, {Platform: plat("linux", "amd64", "")}}, emptyOS},
		"two for the architecture":          {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "amd64", "")}, {Platform: plat("linux", "x86_64", "")}}, -1},
		"two variants":                      {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "amd64", "v3")}, {Platform: plat("linux", "amd64", "v1")}}, -1},
		"only a newer variant":              {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "amd64", "v2")}}, -1},
		"an attestation for the platform":   {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "amd64", ""), Annotations: attestation}}, -1},
		"only another architecture it runs": {amd64Server, []ocispec.Descriptor{{Platform: plat("linux", "386", "")}}, -1},
		"no other OS":                       {amd64Server, []ocispec.Descriptor{{Platform: plat("windows", "amd64", "")}}, -1},
		"no unnamed platform":               {amd64Server, []ocispec.Descriptor{{}, {Platform: plat("linux", "amd64", "")}}, -1},
	} {
		t.Run(name, func(t *testing.T) {
			for i := range tc.children {
				tc.children[i].MediaType = ocispec.MediaTypeImageManifest
				tc.children[i].Digest = digest.FromString(fmt.Sprint(i))
			}
			chosen, err := choosePlatform(tc.children, tc.host)
			switch {
			case tc.chosen < 0 && err == nil:
				t.Fatal("chose", chosen.Platform)
			case tc.chosen >= 0 && (err != nil || chosen.Digest != tc.children[tc.chosen].Digest):
				t.Fatal("chose", chosen.Platform, err)
			}
		})
	}
}

func TestPullRefusesWhatUnpacksBeyondItsLimit(t *testing.T) {
	var empty bytes.Buffer
	w := tar.NewWriter(&empty)
	for i := range 2000 {
		_ = w.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Typeflag: tar.TypeReg, Mode: 0644})
	}
	_ = w.Close()
	small := gzipOf(t, tarOf(t, testFile{"app", 2 << 20}))
	cases := map[string]struct {
		layers [][]byte
		note   string
	}{
		"a highly compressed layer": {[][]byte{gzipOf(t, tarOf(t, testFile{"zeros", 128 << 20}))}, "--image-load-gib"},
		"a layer listed many times": {slices.Repeat([][]byte{small}, 33), "--image-load-gib"},
		"millions of empty files":   {[][]byte{gzipOf(t, empty.Bytes())}, "files and directories"},
		"a sparse file":             {[][]byte{gzipOf(t, sparseTar(8<<30))}, "sparse file"},
		"a polyglot layer":          {[][]byte{gzipTarPolyglot(t)}, "both as compressed and as a plain tar"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := newTestRegistry()
			root := registry.image(tc.layers...)
			_, _, err := measureTest(t, &Engine{}, registry.source(t, nil), root, pullTestLimit)
			if note := refusalNote(t, err); !strings.Contains(note, tc.note) || !strings.HasPrefix(note, "The image registry.example/fixture/app@sha256:0 was not pulled: ") {
				t.Fatal(note)
			}
			archive, err := streamTest(t, registry.source(t, nil), root, pullTestLimit)
			if note := refusalNote(t, err); !strings.Contains(note, tc.note) {
				t.Fatal("loading:", note)
			}
			if bytes.HasSuffix(archive, make([]byte, 1024)) {
				t.Fatal("Docker received the archive's end")
			}
		})
	}
}

// Free space below the reserve is named with its own setting.
func TestPullNamesTheReserve(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"zeros", 128 << 20})))
	limit := pullTestLimit
	limit.bytesByStorage = true
	_, _, err := measureTest(t, &Engine{}, registry.source(t, nil), root, limit)
	if note := refusalNote(t, err); !strings.Contains(note, "--image-load-reserve-gib") || !strings.Contains(note, "deploy again") {
		t.Fatal(note)
	}
}

func TestPullRefusesWhatItCannotMeasure(t *testing.T) {
	layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
	configData := []byte(`{"architecture":"amd64","os":"linux"}`)
	config := descriptorJSON(ocispec.MediaTypeImageConfig, sha(configData), len(configData), "")
	manifest := func(layers string) string {
		return fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]}`, config, layers)
	}
	cases := map[string]struct {
		root   func(r *testRegistry) string
		reason string
	}{
		"a layer with URLs": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), `,"urls":["https://elsewhere.example/layer"]`)))
		}, "other URLs than its registry"},
		"a foreign layer": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON("application/vnd.docker.image.rootfs.foreign.diff.tar.gzip", r.blob(layer), len(layer), "")))
		}, "foreign, non-distributable layer"},
		"an unknown layer type": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON("application/vnd.example.layer+encrypted", r.blob(layer), len(layer), "")))
		}, `media type "application/vnd.example.layer+encrypted"`},
		"an unknown manifest type": {func(r *testRegistry) string {
			return r.manifest("application/vnd.example.manifest", manifest(""))
		}, `media type "application/vnd.example.manifest"`},
		"a config that is no image's": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[]}`, descriptorJSON("application/vnd.example.config", sha(configData), len(configData), "")))
		}, "not an image's"},
		"a config for another platform": {func(r *testRegistry) string {
			return r.imageFor("arm64", layer)
		}, "built for \"linux/arm64\""},
		"a layer that differs from its digest": {func(r *testRegistry) string {
			d := r.blob(layer)
			r.overrides[d] = gzipOf(t, tarOf(t, testFile{"app", 11}))[:len(layer)]
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, d, len(layer), "")))
		}, "differs from its digest"},
		"a layer longer than its descriptor": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer)-1, "")))
		}, "mismatch Content-Length"},
		"layers named twice in different case": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[],"Layers":[%s]}`, config, descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), "")))
		}, "names one field twice"},
		"a digest in different case": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(fmt.Sprintf(`{"mediaType":%q,"digest":%q,"Digest":%q,"size":%d}`, ocispec.MediaTypeImageLayerGzip, r.blob(layer), sha([]byte("x")), len(layer))))
		}, "names one field twice"},
		"a media type the registry does not give": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[]}`, ocispec.MediaTypeImageIndex))
		}, "declares a media type"},
		"an index entry whose registry gives another type": {func(r *testRegistry) string {
			entry := strings.Replace(r.entry(r.image(layer), "linux/amd64", ""), ocispec.MediaTypeImageManifest, "application/vnd.docker.distribution.manifest.v2+json", 1)
			return r.index(entry)
		}, "other than its index does"},
		"a manifest and an index at once": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageIndex, fmt.Sprintf(`{"schemaVersion":2,"manifests":[],"config":%s}`, config))
		}, "not valid for its media type"},
		"a schema 1 manifest": {func(r *testRegistry) string {
			return r.manifest("application/vnd.docker.distribution.manifest.v1+prettyjws", `{"schemaVersion":1}`)
		}, "which Docker does not pull"},
		"no image for this platform": {func(r *testRegistry) string {
			return r.index(r.entry(r.imageFor("arm64", layer), "linux/arm64", ""))
		}, "no image for this server's platform"},
		"a nested index": {func(r *testRegistry) string {
			return r.index(r.entry(r.index(r.entry(r.image(layer), "linux/amd64", "")), "linux/amd64", ""))
		}, `media type "application/vnd.oci.image.index.v1+json"`},
		"another platform's image naming a manifest": {func(r *testRegistry) string {
			inner := r.image(layer)
			other := r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageManifest, inner, len(r.manifests[inner][1]), `,"platform":{"os":"linux","architecture":"amd64"}`)))
			return r.index(r.entry(r.image(layer), "linux/amd64", ""), r.entry(other, "linux/arm64", ""))
		}, "name another manifest or index"},
		"an attestation naming a manifest": {func(r *testRegistry) string {
			chosen := r.image(layer)
			attestation := r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageManifest, chosen, len(r.manifests[chosen][1]), "")))
			return r.index(r.entry(chosen, "linux/amd64", ""), r.entry(attestation, "unknown/unknown", fmt.Sprintf(`{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":%q}`, chosen)))
		}, "name another manifest or index"},
		"an image needing OS features": {func(r *testRegistry) string {
			chosen := r.image(layer)
			return r.index(strings.Replace(r.entry(chosen, "linux/amd64", ""), `"variant":""`, `"variant":"","os.features":["sse4"]`, 1))
		}, "OS version or features"},
		"a sha512 layer": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, "sha512:"+strings.Repeat("ab", 64), len(layer), "")))
		}, "other than a sha256 digest"},
		"bzip2 compression": {func(r *testRegistry) string {
			bz := append([]byte("BZh9"), layer...)
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(bz), len(bz), "")))
		}, "bzip2 compression"},
		"more layers than Docker stacks": {func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(strings.Join(slices.Repeat([]string{descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), "")}, imageChainDepth+1), ",")))
		}, "more layers than Docker can stack"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := newTestRegistry()
			registry.blob(configData)
			d := tc.root(registry)
			_, _, err := measureTest(t, &Engine{}, registry.source(t, nil), d, pullTestLimit)
			var refused *imageLoadRefused
			var unread *imageArchiveUnread
			if !errors.As(err, &refused) && !errors.As(err, &unread) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatal("not refused for its reason", err)
			}
		})
	}
}

// A registry that sends more for a blob than its descriptor says is refused, and Docker gets at
// most the descriptor's size of it and never the archive's end.
func TestPullRefusesABlobLongerThanItsDescriptor(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
	root := registry.image(layer)
	// Too long for one write, so it is sent without a length, as an endless body would be.
	registry.overrides[sha(layer)] = append(append([]byte{}, layer...), make([]byte, 8<<20)...)
	source := registry.source(t, nil)
	archive, err := streamTest(t, source, root, pullTestLimit)
	if note := refusalNote(t, err); !strings.Contains(note, "sent more for a blob than the image's manifest says") {
		t.Fatal(note)
	}
	if len(archive) > 64<<10 {
		t.Fatalf("Docker received %d bytes", len(archive))
	}
}

// What the agent loads into Docker is an archive PLAT-326's gate passes as it is, counting no
// more than the agent counted for it.
func TestPullLoadsAnArchiveTheGatePasses(t *testing.T) {
	registry := newTestRegistry()
	base := gzipOf(t, tarOf(t, testFile{"base", 1 << 20}, testFile{"etc/os", 10}))
	root := registry.image(base, gzipOf(t, tarOf(t, testFile{"app", 10})), base)
	archive, err := streamTest(t, registry.source(t, nil), root, pullTestLimit)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := inspectTest(archive, pullTestLimit)
	if err != nil || !bytes.Equal(docker, archive) {
		t.Fatal("the gate refused or changed the agent's archive", err)
	}
	if !bytes.Contains(archive, []byte(`"io.containerd.image.name":"registry.example/fixture/app@`+root+`"`)) {
		t.Fatal("the loaded image is not named as the deployment pulls it")
	}
	if registry.count(sha(base)) != 1 {
		t.Fatal("a layer listed twice was read twice")
	}
}

// A new version of an image does not download the layers the agent measured for the last.
func TestPullMeasuresEachLayerOnce(t *testing.T) {
	registry := newTestRegistry()
	base := gzipOf(t, tarOf(t, testFile{"base", 1 << 20}))
	first := registry.image(base, gzipOf(t, tarOf(t, testFile{"v1", 10})))
	second := registry.image(base, gzipOf(t, tarOf(t, testFile{"v2", 10})))
	source := registry.source(t, nil)
	engine := &Engine{}
	_, a, err := measureTest(t, engine, source, first, pullTestLimit)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := measureTest(t, engine, source, second, pullTestLimit)
	if err != nil {
		t.Fatal(err)
	}
	if registry.count(sha(base)) != 1 {
		t.Fatal("the base layer was downloaded again")
	}
	if a.Bytes != b.Bytes {
		t.Fatal("a cached layer counted differently", a, b)
	}
}

// The cache of measured layers is keyed by digest alone and holds at most layerCostEntries.
func TestPullLayerCacheIsBounded(t *testing.T) {
	cache := &layerCosts{}
	for i := range layerCostEntries + 10 {
		cache.put(digest.FromString(fmt.Sprint(i)), imageCost{Bytes: int64(i)})
		if len(cache.costs) > layerCostEntries {
			t.Fatal("the cache grew past its bound")
		}
	}
	if cost, ok := cache.get(digest.FromString(fmt.Sprint(layerCostEntries + 9))); !ok || cost.Bytes != layerCostEntries+9 {
		t.Fatal("the latest layer is not cached")
	}
}

// The registry's credential reaches the registry through its token service, and nothing else.
func TestPullReadsAPrivateRegistry(t *testing.T) {
	registry := newTestRegistry()
	registry.username, registry.password = "deployer", "fixture-password"
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	_, _, err := measureTest(t, &Engine{}, registry.source(t, nil), root, pullTestLimit)
	if note := refusalNote(t, err); !strings.Contains(note, "refused to let the agent read it") {
		t.Fatal(note)
	}
	if _, _, err = measureTest(t, &Engine{}, registry.source(t, &pipeline.Credential{Username: "deployer", Password: "fixture-password"}), root, pullTestLimit); err != nil {
		t.Fatal(err)
	}
	if registry.tokens == 0 {
		t.Fatal("no token was issued")
	}
}

func TestRegistrySecureAsDockerDecides(t *testing.T) {
	config := &registrytypes.ServiceConfig{
		InsecureRegistryCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		IndexConfigs:          map[string]*registrytypes.IndexInfo{"docker.io": {Name: "docker.io", Secure: true}, "plain.example:5000": {Name: "plain.example:5000", Secure: false}},
	}
	for name, secure := range map[string]bool{"docker.io": true, "plain.example:5000": false, "10.1.2.3:5000": false, "192.0.2.1": true, "[::1]:5000": true} {
		if registrySecure(context.Background(), config, name) != secure {
			t.Error(name)
		}
	}
	if registrySecure(context.Background(), nil, "127.0.0.1:5000") || !registrySecure(context.Background(), nil, "192.0.2.1:5000") {
		t.Error("Docker's default insecure range")
	}
}

// The agent reaches registries through the daemon's proxy, and never guesses a hidden password.
func TestPullUsesDockersProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	proxy, err := dockerProxy(system.Info{HTTPSProxy: "http://proxy.example:3128", NoProxy: "registry.internal"})
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{"https://registry.example/v2/": "http://proxy.example:3128", "https://registry.internal/v2/": ""} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		got, err := proxy(request)
		if err != nil || (got == nil) != (want == "") || got != nil && got.String() != want {
			t.Error(target, got, err)
		}
	}
	if _, err = dockerProxy(system.Info{HTTPSProxy: "http://xxxxx:xxxxx@proxy.example:3128"}); err == nil || !strings.Contains(err.Error(), "proxy with a password") {
		t.Fatal("used a proxy whose password Docker hides", err)
	}
	t.Setenv("HTTPS_PROXY", "http://agent-proxy.example:3128")
	if _, err = dockerProxy(system.Info{HTTPSProxy: "http://xxxxx:xxxxx@proxy.example:3128"}); err != nil {
		t.Fatal("ignored the agent's own proxy", err)
	}
}

// An insecure registry is read over plain HTTP only when it cannot be reached over HTTPS, so a
// credential the registry refused over HTTPS is never sent in the clear.
func TestPullFallsBackToPlainHTTPOnlyWhenHTTPSIsUnreachable(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	credential := &pipeline.Credential{Username: "deployer", Password: "fixture-password"}
	plain := httptest.NewServer(registry)
	defer plain.Close()
	host := strings.TrimPrefix(plain.URL, "http://")
	fallback, err := newRegistryImage(host, "fixture/app", true, true, credential, http.ProxyFromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	image := fallback.(orasImage)
	image.fallback = true
	refusing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refusing.Close()
	answered, err := newRegistryImage(strings.TrimPrefix(refusing.URL, "https://"), "fixture/app", true, false, credential, http.ProxyFromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = planFromSources(context.Background(), []registryImage{answered, image}, digest.Digest(root), amd64Server, pullTestLimit); err == nil || registry.count(root) != 0 {
		t.Fatal("asked again in the clear after the registry answered over HTTPS", err)
	}
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	unreachable, err := newRegistryImage(strings.TrimPrefix(closed.URL, "https://"), "fixture/app", true, false, credential, http.ProxyFromEnvironment)
	closed.Close()
	if err != nil {
		t.Fatal(err)
	}
	if plan, source, err := planFromSources(context.Background(), []registryImage{unreachable, image}, digest.Digest(root), amd64Server, pullTestLimit); err != nil || plan == nil || source != registryImage(image) {
		t.Fatal("did not fall back to plain HTTP for an unreachable registry", err)
	}
}

func TestDockerAPIVersionComparison(t *testing.T) {
	for v, want := range map[string]bool{"1.48": true, "1.54": true, "2.0": true, "1.47": false, "1.9": false, "": false, "1": false, "x.48": false} {
		if apiVersionAtLeast(v, 1, 48) != want {
			t.Error(v)
		}
	}
}

// failingBlob reads a blob's first bytes from source, then fails, as a registry that drops the
// connection does.
type failingBlob struct{ registryImage }

func (f failingBlob) blob(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := f.registryImage.blob(ctx, desc)
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(io.LimitReader(rc, 100), iotest.ErrReader(errors.New("connection reset"))), rc}, nil
}

// A load's note says why it failed: the registry when the agent could not read it, and Docker's
// own reason when Docker stopped first.
func TestPullLoadNamesWhatFailed(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 1 << 20})))
	source := registry.source(t, nil)
	plan, err := planRegistryImage(context.Background(), source, digest.Digest(root), amd64Server, pullTestLimit)
	if err != nil {
		t.Fatal(err)
	}
	named, _ := reference.ParseNormalizedNamed("registry.example/fixture/app@" + root)
	for name, tc := range map[string]struct {
		source registryImage
		docker func(w http.ResponseWriter, r *http.Request)
		note   string
	}{
		"the registry fails": {failingBlob{source}, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			fmt.Fprint(w, `{"error":"unexpected EOF"}`)
		}, "could not read it from its registry"},
		"Docker fails": {source, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.CopyN(io.Discard, r.Body, 1024)
			fmt.Fprint(w, `{"error":"write /var/lib/containerd: no space left on device"}`)
		}, "Docker's image load failed: write /var/lib/containerd: no space left on device"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Api-Version", "1.54")
				switch {
				case strings.HasSuffix(r.URL.Path, "/_ping"):
				case strings.HasSuffix(r.URL.Path, "/images/load"):
					tc.docker(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost("tcp://" + strings.TrimPrefix(server.URL, "http://")))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			err = (&Engine{Docker: docker}).loadRegistryImage(context.Background(), named.(reference.Canonical), tc.source, plan, pullTestLimit)
			if note := refusalNote(t, err); !strings.Contains(note, tc.note) {
				t.Fatal(note)
			}
		})
	}
}
