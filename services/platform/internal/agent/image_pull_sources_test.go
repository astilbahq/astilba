package agent

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/system"
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

// An image's sources are all read again when one that may recover kept the agent from reading it,
// and only then.
func TestPullReadsItsSourcesAgain(t *testing.T) {
	fail := func(code int, header string, always bool) func(http.ResponseWriter, *http.Request, string, int) bool {
		return func(w http.ResponseWriter, req *http.Request, _ string, n int) bool {
			if !strings.Contains(req.URL.Path, "/manifests/") || n > 1 && !always {
				return false
			}
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			w.WriteHeader(code)
			return true
		}
	}
	short := func(w http.ResponseWriter, req *http.Request, _ string, _ int) bool {
		if !strings.Contains(req.URL.Path, "/blobs/") {
			return false
		}
		_ = http.NewResponseController(w).Flush()
		_, _ = io.WriteString(w, "{}")
		return true
	}
	misnamed := func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
		w.Header().Set("Docker-Content-Digest", sha([]byte("other")))
		return false
	}
	for name, tc := range map[string]struct {
		mirror, registry func(http.ResponseWriter, *http.Request, string, int) bool
		distrusted       bool
		read             bool
		mirrors, reads   int
	}{
		"its mirror failing once, its registry always":            {fail(503, "", false), fail(403, "", true), true, true, 2, 0},
		"its registry's own answer, after its mirror failed":      {fail(503, "", true), fail(403, "", true), false, false, 1, 1},
		"its mirror's answer, before its registry fails once":     {fail(404, "", true), fail(503, "", false), false, true, 2, 2},
		"its registry's short config, after its mirror failed":    {fail(503, "", true), short, false, false, 1, 1},
		"its registry's misnamed config, after its mirror failed": {fail(503, "", true), misnamed, false, false, 1, 1},
		"its registry failing once, its mirror asking for a wait": {fail(503, "3600", true), fail(503, "", false), false, true, 2, 2},
		"its registry asking for a wait, its mirror failing":      {fail(503, "", true), fail(503, "3600", true), false, false, 1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			mirror, registry := newTestRegistry(), newTestRegistry()
			layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
			root := mirror.image(layer)
			registry.image(layer)
			mirror.fault, registry.fault = tc.mirror, tc.registry
			sources := []registryImage{mirror.source(t, nil), registry.source(t, nil)}
			if tc.distrusted {
				server := httptest.NewUnstartedServer(registry)
				server.Config.ErrorLog = log.New(io.Discard, "", 0)
				server.StartTLS()
				defer server.Close()
				distrusted, err := newRegistryImage(strings.TrimPrefix(server.URL, "https://"), "fixture/app", false, false, nil, http.ProxyFromEnvironment)
				if err != nil {
					t.Fatal(err)
				}
				sources[1] = distrusted
			}
			if err := readSources(t, sources, root); (err == nil) != tc.read {
				t.Fatal(err)
			}
			if m, r := mirror.count(root), registry.count(root); m != tc.mirrors || r != tc.reads {
				t.Fatalf("read the mirror %d times and the registry %d", m, r)
			}
		})
	}
}

// readSources reads the image from sources as a pull into the classic store does.
func readSources(t *testing.T, sources []registryImage, root string) error {
	t.Helper()
	engine := &Engine{registryRetry: quickRegistryRetry()}
	return engine.readRegistry(t.Context(), "fixture/app", func(ctx context.Context) error {
		plan, source, err := planFromSources(ctx, sources, digest.Digest(root), amd64Server, pullTestLimit, false, nil)
		if err != nil {
			return err
		}
		_, err = engine.measureRegistryImage(ctx, source, plan, pullTestLimit)
		return err
	})
}

// insecureRegistry is registry on a server that speaks only TLS, as Docker reads a registry it
// treats as insecure: over HTTPS, then over plain HTTP, which the server answers with 400.
func insecureRegistry(t *testing.T, registry *testRegistry) []registryImage {
	t.Helper()
	server := httptest.NewUnstartedServer(registry)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	named, err := reference.ParseNormalizedNamed(strings.TrimPrefix(server.URL, "https://") + "/fixture/app")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := registryImages(t.Context(), system.Info{}, named, nil)
	if err != nil || len(sources) != 2 {
		t.Fatal(len(sources), err)
	}
	return sources
}

// An insecure registry that drops a connection is read again over HTTPS, whatever it answers in
// the clear.
func TestPullReadsAnInsecureRegistryAgainOverHTTPS(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	registry.fault = func(w http.ResponseWriter, _ *http.Request, d string, n int) bool {
		if d == root && n == 1 {
			hijacked(t, w, nil, false)
			return true
		}
		return false
	}
	if err := readSources(t, insecureRegistry(t, registry), root); err != nil {
		t.Fatal(err)
	}
	if n := registry.count(root); n != 2 {
		t.Fatalf("read %d times", n)
	}
}

// A registry that has answered over HTTPS is not asked in the clear by a later attempt, so a
// connection cut by someone else can't have it send a credential in the clear.
func TestPullNeverAsksInTheClearAfterAnAnswerOverHTTPS(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
	root := registry.image(layer)
	registry.fault = func(w http.ResponseWriter, _ *http.Request, d string, n int) bool {
		if d == sha(layer) || d == root && n > 1 {
			hijacked(t, w, nil, false)
			return true
		}
		return false
	}
	sources := insecureRegistry(t, registry)
	clear := newTestRegistry()
	clear.image(layer)
	plain := clear.source(t, nil).(orasImage)
	plain.https = sources[1].(orasImage).https
	sources[1] = plain
	if err := readSources(t, sources, root); err == nil {
		t.Fatal("read")
	}
	if n := registry.count(root); n != 3 {
		t.Fatalf("read %d times over HTTPS", n)
	}
	if n := clear.count(root) + clear.count(sha(layer)); n != 0 {
		t.Fatalf("read %d times in the clear", n)
	}
}

// A registry whose connection stalls is read again over another, and one read after its window
// ended is the last.
func TestPullReadsAgainAfterAStall(t *testing.T) {
	for name, proto := range map[string]string{"HTTP/1.1": "HTTP/1.1", "HTTP/2": "HTTP/2.0", "HTTP/2 during an answer of no set length": "HTTP/2.0"} {
		t.Run(name, func(t *testing.T) {
			// An answer of no set length is asked for again, so it is read twice when it stalls.
			asks, unsized := 1, strings.HasSuffix(name, "length")
			if unsized {
				asks = 2
			}
			registry := newTestRegistry()
			root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
			released := make(chan struct{})
			var stalled atomic.Pointer[net.Conn]
			registry.fault = func(w http.ResponseWriter, req *http.Request, d string, _ int) bool {
				if req.Proto != proto {
					t.Error("the registry was read over", req.Proto)
				}
				conn := req.Context().Value(connKey{}).(net.Conn)
				stalled.CompareAndSwap(nil, &conn)
				if *stalled.Load() == conn {
					if d == root && req.Method == http.MethodGet && unsized {
						w.Header().Set("Content-Type", registry.manifests[root][0])
						w.Header().Set("Docker-Content-Digest", root)
						_ = http.NewResponseController(w).Flush()
					}
					<-released
				}
				return false
			}
			server := httptest.NewUnstartedServer(registry)
			server.EnableHTTP2 = proto == "HTTP/2.0"
			server.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
				return context.WithValue(ctx, connKey{}, c)
			}
			server.StartTLS()
			defer server.Close()
			defer close(released)
			source, err := newRegistryImage(strings.TrimPrefix(server.URL, "https://"), "fixture/app", true, false, nil, http.ProxyFromEnvironment)
			if err != nil {
				t.Fatal(err)
			}
			source.(orasImage).transport.ResponseHeaderTimeout = 200 * time.Millisecond
			read := func(ctx context.Context) error {
				_, err := planRegistryImage(ctx, source, digest.Digest(root), amd64Server, pullTestLimit, true, nil)
				return err
			}
			engine := &Engine{registryRetry: quickRegistryRetry()}
			if err := engine.readRegistry(t.Context(), "fixture/app", read); err != nil {
				t.Fatal(err)
			}
			if n := registry.count(root); n != asks+1 {
				t.Fatalf("read %d times", n)
			}
			engine.registryRetry.window = 100 * time.Millisecond
			stalled.Store(nil)
			if err := engine.readRegistry(t.Context(), "fixture/app", read); err == nil {
				t.Fatal("read again after its window")
			}
			if n := registry.count(root); n != 2*asks+1 {
				t.Fatalf("read %d times", n)
			}
		})
	}
}

// An insecure registry's own answer in the clear stands, whatever kept it from HTTPS.
func TestPullKeepsAnInsecureRegistrysAnswer(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	registry.fault = func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
		w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	unreachable, err := newRegistryImage(strings.TrimPrefix(closed.URL, "https://"), "fixture/app", true, false, nil, http.ProxyFromEnvironment)
	closed.Close()
	if err != nil {
		t.Fatal(err)
	}
	clear := registry.source(t, nil).(orasImage)
	clear.https = unreachable.(orasImage).transport.answers
	err = readSources(t, []registryImage{unreachable, clear}, root)
	if err == nil || !strings.Contains(err.Error(), "Check the image's registry, its credentials") {
		t.Fatal(err)
	}
	if n := registry.count(root); n != 1 {
		t.Fatalf("read %d times", n)
	}
}

// What a proxy answers when asked to connect to a registry decides by its status alone.
func TestPullThroughAProxyThatRefuses(t *testing.T) {
	for answer, want := range map[string]int32{
		"503 Service Unavailable": 3,
		"403 Forbidden":           1,
		"403 http2: server sent GOAWAY and closed the connection": 1,
	} {
		t.Run(answer, func(t *testing.T) {
			var asked atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				asked.Add(1)
				hijacked(t, w, []byte("HTTP/1.1 "+answer+"\r\nContent-Length: 0\r\n\r\n"), false)
			}))
			defer proxy.Close()
			address, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			source, err := newRegistryImage("registry.example", "fixture/app", false, false, nil, http.ProxyURL(address))
			if err != nil {
				t.Fatal(err)
			}
			if err = readSources(t, []registryImage{source}, sha([]byte("fixture"))); err == nil {
				t.Fatal("read")
			}
			if n := asked.Load(); n != want {
				t.Fatalf("asked %d times", n)
			}
		})
	}
}

// A mirror that serves an image's manifests but fails its blobs gives way to the registry itself
// for the rest of the pull, so the retries reach the registry.
func TestPullPassesOverAMirrorThatFailsBlobs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the agent counts what Docker's storage holds on Linux alone")
	}
	for name, fail := range map[string]func(w http.ResponseWriter){
		"a server error": func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		// Neither is the registry's answer.
		"a missing blob": func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) },
		"a long wait": func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
		},
	} {
		for _, store := range []string{"containerd", "classic"} {
			t.Run(name+"/"+store, func(t *testing.T) {
				mirror, registry := newTestRegistry(), newTestRegistry()
				layer := gzipOf(t, tarOf(t, testFile{"app", 10}))
				root := mirror.image(layer)
				registry.image(layer)
				mirror.fault = func(w http.ResponseWriter, _ *http.Request, d string, _ int) bool {
					if d != sha(layer) {
						return false
					}
					fail(w)
					return true
				}
				passed := mirror.source(t, nil).(orasImage)
				passed.mirror = true
				id := root
				if store == "classic" {
					id = sha([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers"}}`))
				}
				engine := &Engine{Docker: pullDocker(t, store == "containerd", id), ImageLoadReserveBytes: 1, registryRetry: quickRegistryRetry()}
				named, err := reference.ParseNormalizedNamed("registry.example/fixture/app@" + root)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = engine.readImage(t.Context(), named.(reference.Canonical), []registryImage{passed, registry.source(t, nil)}, amd64Server, store == "containerd"); err != nil {
					t.Fatal(err)
				}
				if m, r := mirror.count(sha(layer)), registry.count(sha(layer)); m != 1 || r != 1 {
					t.Fatalf("read the layer from the mirror %d times and from the registry %d", m, r)
				}
			})
		}
	}
}

// Only a mirror is passed over, never the registry itself.
func TestPassOverOnlyAFailingMirror(t *testing.T) {
	registry := newTestRegistry()
	mirror := registry.source(t, nil).(orasImage)
	mirror.mirror = true
	itself := registry.source(t, nil)
	sources := []registryImage{mirror, itself}
	unread := &imageArchiveUnread{errors.New("fixture")}
	if got := passOver(sources, mirror, unread); len(got) != 1 || got[0].(orasImage).repo != itself.(orasImage).repo {
		t.Fatal("the mirror was not passed over")
	}
	if got := passOver(sources, itself, unread); len(got) != 2 {
		t.Fatal("the registry itself was passed over")
	}
}
