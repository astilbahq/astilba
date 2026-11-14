package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

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
	plan, err := planRegistryImage(context.Background(), source, digest.Digest(root), amd64Server, pullTestLimit, true, nil)
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

// failingDigest fails reading one blob after its first bytes.
type failingDigest struct {
	registryImage
	digest digest.Digest
}

func (f failingDigest) blob(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	if desc.Digest != f.digest {
		return f.registryImage.blob(ctx, desc)
	}
	return failingBlob{f.registryImage}.blob(ctx, desc)
}

// fakeDocker answers as Docker of the given API version, failing any load with its message.
func fakeDocker(t *testing.T, api string, load func(w http.ResponseWriter, r *http.Request)) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", api)
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
		case strings.HasSuffix(r.URL.Path, "/version"):
			fmt.Fprintf(w, `{"ApiVersion":%q,"Version":"x"}`, api)
		case strings.HasSuffix(r.URL.Path, "/images/load"):
			load(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	docker, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
}

// Loading an image of an index needs Docker 28, and a registry failing during an attestation is
// named as the registry's failure.
func TestPullLoadOfAnIndex(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 1 << 20}))
	chosen := registry.image(layer)
	statement := make([]byte, 64<<10)
	attestation := registry.manifest(ocispec.MediaTypeImageManifest, fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]}`, descriptorJSON(ocispec.MediaTypeImageConfig, registry.blob([]byte("{}")), 2, ""), descriptorJSON("application/vnd.in-toto+json", registry.blob(statement), len(statement), "")))
	root := registry.index(registry.entry(chosen, "linux/amd64", ""), registry.entry(attestation, "unknown/unknown", fmt.Sprintf(`{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":%q}`, chosen)))
	source := registry.source(t, nil)
	plan, err := planRegistryImage(context.Background(), source, digest.Digest(root), amd64Server, pullTestLimit, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	named, _ := reference.ParseNormalizedNamed("registry.example/fixture/app@" + root)
	drain := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"error":"unexpected EOF"}`)
	}
	for name, tc := range map[string]struct {
		api    string
		source registryImage
		note   string
	}{
		"on Docker 27":                     {"1.47", source, "older than version 28"},
		"when the registry fails mid-blob": {"1.54", failingDigest{source, digest.Digest(sha(statement))}, "could not read it from its registry"},
	} {
		t.Run(name, func(t *testing.T) {
			before := registry.count(sha(layer))
			err := (&Engine{Docker: fakeDocker(t, tc.api, drain)}).loadRegistryImage(context.Background(), named.(reference.Canonical), tc.source, plan, pullTestLimit)
			if note := refusalNote(t, err); !strings.Contains(note, tc.note) {
				t.Fatal(note)
			}
			// Docker that cannot load one platform gets no stream at all.
			if tc.api == "1.47" && registry.count(sha(layer)) != before {
				t.Fatal("streamed an image to a Docker that cannot load it")
			}
		})
	}
}
