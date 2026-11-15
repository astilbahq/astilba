package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

// A Docker Hub image read without a credential, through the token Docker Hub issues anonymously.
func TestPullReadsThroughAnAnonymousToken(t *testing.T) {
	registry := newTestRegistry()
	registry.anonymous = true
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	if _, _, err := measureTest(t, &Engine{}, registry.source(t, nil), root, pullTestLimit); err != nil {
		t.Fatal(err)
	}
	if registry.tokens == 0 {
		t.Fatal("no anonymous token was issued")
	}
}

// A mirror that lacks one of an image's index entries gives way to the next source.
func TestPullFallsBackPastAMirrorLackingAnEntry(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
	chosen, other := registry.image(layer), registry.imageFor("arm64", layer)
	root := registry.index(registry.entry(chosen, "linux/amd64", ""), registry.entry(other, "linux/arm64", ""))
	mirror := newTestRegistry()
	for d, m := range registry.manifests {
		if d != other {
			mirror.manifests[d] = m
		}
	}
	mirror.blobs = registry.blobs
	plan, _, err := planFromSources(context.Background(), []registryImage{mirror.source(t, nil), registry.source(t, nil)}, digest.Digest(root), amd64Server, pullTestLimit, true, nil)
	if err != nil || plan == nil || mirror.count(other) != 1 || registry.count(other) != 1 {
		t.Fatal("did not fall back past the mirror", err)
	}
	if _, _, err = planFromSources(context.Background(), []registryImage{mirror.source(t, nil)}, digest.Digest(root), amd64Server, pullTestLimit, true, nil); err == nil || !strings.Contains(err.Error(), "which its registry no longer has") {
		t.Fatal("the last source's lack was not named", err)
	}
}

// A mirror that refuses requests, as a rate limit does, or that can't be reached, gives way to
// the registry.
func TestPullFallsBackPastAFailingMirror(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	t.Cleanup(limited.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, server := range map[string]*httptest.Server{"rate limited": limited, "unreachable": closed} {
		t.Run(name, func(t *testing.T) {
			mirror, err := newRegistryImage(strings.TrimPrefix(server.URL, "http://"), "fixture/app", true, true, nil, http.ProxyFromEnvironment)
			if err != nil {
				t.Fatal(err)
			}
			before := registry.count(root)
			plan, _, err := planFromSources(context.Background(), []registryImage{mirror, registry.source(t, nil)}, digest.Digest(root), amd64Server, pullTestLimit, false, nil)
			if err != nil || plan == nil || registry.count(root) == before {
				t.Fatal("did not fall back past the mirror", err)
			}
		})
	}
}
