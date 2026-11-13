package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	registrytypes "github.com/moby/moby/api/types/registry"
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

// image adds a manifest of these gzip layers and returns its digest.
func (r *testRegistry) image(layers ...[]byte) string {
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers"}}`)
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
		os, arch, _ := strings.Cut(platform, "/")
		extra += fmt.Sprintf(`,"platform":{"os":%q,"architecture":%q}`, os, arch)
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
	image, err := newRegistryImage(strings.TrimPrefix(server.URL, "http://"), "fixture/app", true, true, credential)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

var pullTestLimit = imageLoadLimit{Bytes: 64 << 20, Entries: 1000, loadBytes: 8 << 30, reserveBytes: 4 << 30}

func measureTest(t *testing.T, source registryImage, root string, limit imageLoadLimit, cache *layerCosts) (imageMeasure, error) {
	t.Helper()
	m := imageMeasure{source: source, limit: limit, platforms: []string{"amd64"}, cache: cache}
	err := m.image(context.Background(), digest.Digest(root))
	return m, err
}

func refusalNote(t *testing.T, err error) string {
	t.Helper()
	var refused *imageLoadRefused
	if !errors.As(err, &refused) {
		t.Fatalf("not refused: %v", err)
	}
	return refused.pulled("registry.example/fixture/app@sha256:0")
}

// An image pulls when it is within its limit, counting only what Docker pulls on this server.
func TestPullMeasuresOnlyThisServersImage(t *testing.T) {
	registry := newTestRegistry()
	app := gzipOf(t, tarOf(t, testFile{"app", 10 << 10}))
	other := gzipOf(t, tarOf(t, testFile{"other", 10 << 10}))
	amd64 := registry.image(app)
	arm64 := registry.image(other)
	attestation := registry.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]}`, descriptorJSON("application/vnd.in-toto+json", registry.blob([]byte("{}")), 2, ""), descriptorJSON("application/vnd.in-toto+json", registry.blob([]byte(`{"x":1}`)), 7, "")))
	root := registry.index(registry.entry(amd64, "linux/amd64", ""), registry.entry(arm64, "linux/arm64", ""), registry.entry(attestation, "unknown/unknown", fmt.Sprintf(`{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":%q}`, amd64)))
	m, err := measureTest(t, registry.source(t, nil), root, pullTestLimit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if registry.count(arm64) != 0 || registry.count(sha(other)) != 0 {
		t.Fatal("another platform's image was read")
	}
	if registry.count(attestation) != 1 {
		t.Fatal("the attestation Docker may store was not counted")
	}
	if len(m.chains) != 1 || m.chains[0][0] != sha(app) {
		t.Fatal("unexpected chains", m.chains)
	}
	// Four documents and five blobs stored, and the layer unpacked.
	if m.stored.Entries != 7 {
		t.Fatal("stored blobs", m.stored)
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
		"a layer listed many times": {[][]byte{small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small, small}, "--image-load-gib"},
		"millions of empty files":   {[][]byte{gzipOf(t, empty.Bytes())}, "files and directories"},
		"a sparse file":             {[][]byte{gzipOf(t, sparseTar(8<<30))}, "sparse file"},
		"a polyglot layer":          {[][]byte{gzipTarPolyglot(t)}, "both as compressed and as a plain tar"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := newTestRegistry()
			root := registry.image(tc.layers...)
			_, err := measureTest(t, registry.source(t, nil), root, pullTestLimit, nil)
			if note := refusalNote(t, err); !strings.Contains(note, tc.note) || !strings.HasPrefix(note, "The image registry.example/fixture/app@sha256:0 was not pulled: ") {
				t.Fatal(note)
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
	_, err := measureTest(t, registry.source(t, nil), root, limit, nil)
	if note := refusalNote(t, err); !strings.Contains(note, "--image-load-reserve-gib") || !strings.Contains(note, "deploy again") {
		t.Fatal(note)
	}
}

func TestPullRefusesWhatItCannotMeasure(t *testing.T) {
	layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
	config := descriptorJSON(ocispec.MediaTypeImageConfig, sha([]byte("{}")), 2, "")
	manifest := func(layers string) string {
		return fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]}`, config, layers)
	}
	cases := map[string]func(r *testRegistry) string{
		"a foreign layer": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), `,"urls":["https://elsewhere.example/layer"]`)))
		},
		"a non-distributable layer": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON("application/vnd.docker.image.rootfs.foreign.diff.tar.gzip", r.blob(layer), len(layer), "")))
		},
		"a layer that differs from its digest": func(r *testRegistry) string {
			d := r.blob(layer)
			r.overrides[d] = gzipOf(t, tarOf(t, testFile{"app", 11}))[:len(layer)]
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, d, len(layer), "")))
		},
		"a layer longer than its descriptor": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer)-1, "")))
		},
		"layers named twice in different case": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[],"Layers":[%s]}`, config, descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), "")))
		},
		"a digest in different case": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(fmt.Sprintf(`{"mediaType":%q,"digest":%q,"Digest":%q,"size":%d}`, ocispec.MediaTypeImageLayerGzip, r.blob(layer), sha([]byte("x")), len(layer))))
		},
		"a media type the registry does not give": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[]}`, ocispec.MediaTypeImageIndex))
		},
		"a manifest and an index at once": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageIndex, fmt.Sprintf(`{"schemaVersion":2,"manifests":[],"config":%s}`, config))
		},
		"a schema 1 manifest": func(r *testRegistry) string {
			return r.manifest("application/vnd.docker.distribution.manifest.v1+prettyjws", `{"schemaVersion":1}`)
		},
		"no image for this platform": func(r *testRegistry) string {
			return r.index(r.entry(r.image(layer), "linux/arm64", ""))
		},
		"a sha512 layer": func(r *testRegistry) string {
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, "sha512:"+strings.Repeat("ab", 64), len(layer), "")))
		},
		"bzip2 compression": func(r *testRegistry) string {
			bz := append([]byte("BZh9"), layer...)
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(bz), len(bz), "")))
		},
		"an index nested too deeply": func(r *testRegistry) string {
			d := r.image(layer)
			for range imageIndexDepth + 1 {
				d = r.index(r.entry(d, "", ""))
			}
			return d
		},
		"more layers than Docker stacks": func(r *testRegistry) string {
			descriptors := []string{}
			for range imageChainDepth + 1 {
				descriptors = append(descriptors, descriptorJSON(ocispec.MediaTypeImageLayerGzip, r.blob(layer), len(layer), ""))
			}
			return r.manifest(ocispec.MediaTypeImageManifest, manifest(strings.Join(descriptors, ",")))
		},
	}
	reasons := map[string]string{
		"a foreign layer":                         "download from elsewhere",
		"a non-distributable layer":               "foreign.diff.tar.gzip\", which the agent does not pull",
		"a layer that differs from its digest":    "differs from its digest",
		"a layer longer than its descriptor":      "mismatch Content-Length",
		"layers named twice in different case":    "names one field twice",
		"a digest in different case":              "names one field twice",
		"a media type the registry does not give": "declares a media type",
		"a manifest and an index at once":         "of a kind Docker does not pull",
		"a schema 1 manifest":                     "of a kind Docker does not pull",
		"no image for this platform":              "no image for this server's platform",
		"a sha512 layer":                          "other than a sha256 digest",
		"bzip2 compression":                       "bzip2 compression",
		"an index nested too deeply":              "nested too deeply",
		"more layers than Docker stacks":          "more layers than Docker can stack",
	}
	for name, root := range cases {
		t.Run(name, func(t *testing.T) {
			registry := newTestRegistry()
			registry.blob([]byte("{}"))
			d := root(registry)
			_, err := measureTest(t, registry.source(t, nil), d, pullTestLimit, nil)
			var refused *imageLoadRefused
			var unread *imageArchiveUnread
			if !errors.As(err, &refused) && !errors.As(err, &unread) || !strings.Contains(err.Error(), reasons[name]) {
				t.Fatal("not refused for its reason", err)
			}
		})
	}
}

// A new version of an image does not download the layers the agent measured for the last.
func TestPullMeasuresEachLayerOnce(t *testing.T) {
	registry := newTestRegistry()
	base := gzipOf(t, tarOf(t, testFile{"base", 1 << 20}))
	first := registry.image(base, gzipOf(t, tarOf(t, testFile{"v1", 10})))
	second := registry.image(base, gzipOf(t, tarOf(t, testFile{"v2", 10})))
	source := registry.source(t, nil)
	cache := &layerCosts{}
	a, err := measureTest(t, source, first, pullTestLimit, cache)
	if err != nil {
		t.Fatal(err)
	}
	b, err := measureTest(t, source, second, pullTestLimit, cache)
	if err != nil {
		t.Fatal(err)
	}
	if registry.count(sha(base)) != 1 {
		t.Fatal("the base layer was downloaded again")
	}
	if a.unpacked[sha(base)] != b.unpacked[sha(base)] {
		t.Fatal("a cached layer counted differently")
	}
}

// The registry's credential reaches the registry through its token service, and nothing else.
func TestPullReadsAPrivateRegistry(t *testing.T) {
	registry := newTestRegistry()
	registry.username, registry.password = "deployer", "fixture-password"
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	if _, err := measureTest(t, registry.source(t, nil), root, pullTestLimit, nil); err == nil {
		t.Fatal("read a private image without its credential")
	} else if note := refusalNote(t, registryUnreachable(err.(*imageArchiveUnread).err)); !strings.Contains(note, "refused to let the agent read it") {
		t.Fatal(note)
	}
	if _, err := measureTest(t, registry.source(t, &pipeline.Credential{Username: "deployer", Password: "fixture-password"}), root, pullTestLimit, nil); err != nil {
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
		if registrySecure(config, name) != secure {
			t.Error(name)
		}
	}
	if registrySecure(nil, "127.0.0.1:5000") || !registrySecure(nil, "192.0.2.1:5000") {
		t.Error("Docker's default insecure range")
	}
}
