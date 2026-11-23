package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"golang.org/x/net/http2"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func quickRegistryRetry() *registryRetry {
	return &registryRetry{attempts: 3, backoff: []time.Duration{time.Millisecond, 2 * time.Millisecond}, window: 30 * time.Second}
}

func never() bool { return false }

// registryPull is a registry over HTTPS, which Docker trusts, with an image of one layer, and the
// Docker that pulls it.
type registryPull struct {
	*testRegistry
	image, root, layer string
	credential         *pipeline.Credential
	engine             *Engine
	logged             *bytes.Buffer
}

func newRegistryPull(t *testing.T, containerd bool, proto string) *registryPull {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the agent counts what Docker's storage holds on Linux alone")
	}
	layer := gzipOf(t, tarOf(t, testFile{"app", 4 << 20}))
	p := &registryPull{testRegistry: newTestRegistry(), layer: sha(layer), logged: captureTrail(t)}
	p.root = p.testRegistry.image(layer)
	server := httptest.NewUnstartedServer(p.testRegistry)
	server.EnableHTTP2 = proto == "HTTP/2.0"
	server.StartTLS()
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "https://")
	certs := dockerCertsDirectory
	dockerCertsDirectory = t.TempDir()
	t.Cleanup(func() { dockerCertsDirectory = certs })
	if err := os.Mkdir(filepath.Join(dockerCertsDirectory, host), 0700); err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dockerCertsDirectory, host, "ca.crt"), ca, 0600); err != nil {
		t.Fatal(err)
	}
	p.image = host + "/fixture/app@" + p.root
	id := p.root
	if !containerd {
		id = sha([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers"}}`))
	}
	p.engine = &Engine{Docker: pullDocker(t, containerd, id), ImageLoadReserveBytes: 1, registryRetry: quickRegistryRetry()}
	return p
}

func (p *registryPull) pull(ctx context.Context) error {
	return p.engine.pullMeasuredImage(ctx, p.image, p.credential, never)
}

// note is the pull's refusal.
func (p *registryPull) note(t *testing.T) string {
	t.Helper()
	var refused *imagePullRefused
	if err := p.pull(t.Context()); !errors.As(err, &refused) {
		t.Fatalf("not refused: %v", err)
	}
	return refused.Error()
}

// pullDocker answers as Docker with either image store, its storage in a scratch directory. It
// takes what it loads or pulls, and has the image id names.
func pullDocker(t *testing.T, containerd bool, id string) *client.Client {
	t.Helper()
	root := t.TempDir()
	// A socket's path may be far shorter than a test's own directory.
	sockets, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	listener, err := net.Listen("unix", filepath.Join(sockets, "s"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.54")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
		case strings.HasSuffix(r.URL.Path, "/info"):
			info := system.Info{OSType: "linux", Architecture: "x86_64", DockerRootDir: root, RegistryConfig: &registrytypes.ServiceConfig{}}
			if containerd {
				info.DriverStatus = [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}
			}
			_ = json.MarshalWrite(w, info)
		case strings.HasSuffix(r.URL.Path, "/images/load"):
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				fmt.Fprint(w, `{"error":"unexpected EOF"}`)
			}
		case strings.HasSuffix(r.URL.Path, "/images/create"):
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprintf(w, `{"Id":%q}`, id)
		default:
			http.NotFound(w, r)
		}
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	docker, err := client.New(client.WithHost("unix://" + listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
}

// hijacked closes the request's connection, resetting it when reset, before or after part of an
// answer.
func hijacked(t *testing.T, w http.ResponseWriter, answer []byte, reset bool) {
	t.Helper()
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	_, _ = conn.Write(answer)
	if reset {
		// The connection's own close would send a TLS alert first.
		if tcp, ok := conn.(interface{ NetConn() net.Conn }); ok {
			_ = tcp.NetConn().(*net.TCPConn).SetLinger(0)
			conn = tcp.NetConn()
		}
	}
	_ = conn.Close()
}

// A registry that fails once, for a reason it recovers from, does not fail the pull.
func TestPullReadsAgainAfterARegistryRecovers(t *testing.T) {
	status := func(code int, header ...string) func(*testing.T, *registryPull, http.ResponseWriter) {
		return func(_ *testing.T, _ *registryPull, w http.ResponseWriter) {
			for i := 0; i < len(header); i += 2 {
				w.Header().Set(header[i], header[i+1])
			}
			w.WriteHeader(code)
		}
	}
	partial := func(p *registryPull) []byte {
		data := p.blobs[p.layer]
		return append(fmt.Appendf(nil, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(data)), data[:len(data)/2]...)
	}
	for name, tc := range map[string]struct {
		proto    string
		manifest bool
		fail     func(t *testing.T, p *registryPull, w http.ResponseWriter)
	}{
		"unavailable":               {"HTTP/1.1", true, status(http.StatusServiceUnavailable)},
		"internal error":            {"HTTP/1.1", false, status(http.StatusInternalServerError)},
		"request timeout":           {"HTTP/1.1", false, status(http.StatusRequestTimeout)},
		"too many requests":         {"HTTP/1.1", true, status(http.StatusTooManyRequests, "Retry-After", "0")},
		"a storage gateway's error": {"HTTP/1.1", false, status(530)},
		"a blob cut short":          {"HTTP/1.1", false, func(t *testing.T, p *registryPull, w http.ResponseWriter) { hijacked(t, w, partial(p), false) }},
		"a blob's connection reset": {"HTTP/1.1", false, func(t *testing.T, p *registryPull, w http.ResponseWriter) { hijacked(t, w, partial(p), true) }},
		"an HTTP/2 stream reset": {"HTTP/2.0", false, func(_ *testing.T, p *registryPull, w http.ResponseWriter) {
			data := p.blobs[p.layer]
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data[:len(data)/2])
			_ = http.NewResponseController(w).Flush()
			panic(http.ErrAbortHandler)
		}},
	} {
		for _, store := range []string{"containerd", "classic"} {
			t.Run(name+"/"+store, func(t *testing.T) {
				p := newRegistryPull(t, store == "containerd", tc.proto)
				failing := p.layer
				if tc.manifest {
					failing = p.root
				}
				p.fault = func(w http.ResponseWriter, req *http.Request, digest string, n int) bool {
					if req.Proto != tc.proto {
						t.Error("the registry was read over", req.Proto)
					}
					if digest != failing || n > 1 {
						return false
					}
					tc.fail(t, p, w)
					return true
				}
				if err := p.pull(t.Context()); err != nil {
					t.Fatal(err)
				}
				if n := p.count(failing); n != 2 {
					t.Fatalf("read %d times", n)
				}
				if log := p.logged.String(); strings.Count(log, "could not read an image") != 1 || !strings.Contains(log, "readsAgain=true") {
					t.Fatal(log)
				}
			})
		}
	}
}

// A registry that gives a definite answer, or other content than the image names, is not asked
// again.
func TestPullDoesNotReadAgainWhatWontChange(t *testing.T) {
	for name, tc := range map[string]struct {
		fail func(p *registryPull) func(w http.ResponseWriter, req *http.Request, digest string, n int) bool
		note string
	}{
		"unauthorized": {func(*registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			return func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
				w.WriteHeader(http.StatusUnauthorized)
				return true
			}
		}, "its registry refused to let the agent read it"},
		"forbidden": {func(*registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			return func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
				w.WriteHeader(http.StatusForbidden)
				return true
			}
		}, "its registry refused to let the agent read it"},
		"not found": {func(*registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			return func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
		}, "its registry does not have it"},
		"a layer that differs": {func(p *registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			p.overrides[p.layer] = bytes.Clone(p.blobs[p.layer])
			p.overrides[p.layer][len(p.overrides[p.layer])-1] ^= 1
			return nil
		}, "which the agent cannot measure safely"},
		"a layer that goes on": {func(p *registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			return func(w http.ResponseWriter, _ *http.Request, digest string, _ int) bool {
				if digest != p.layer {
					return false
				}
				_ = http.NewResponseController(w).Flush()
				_, _ = w.Write(append(bytes.Clone(p.blobs[p.layer]), make([]byte, 1<<10)...))
				return true
			}
		}, "its registry sent more for a blob"},
		"an image over its limit": {func(p *registryPull) func(http.ResponseWriter, *http.Request, string, int) bool {
			p.engine.ImageLoadBytes = 1 << 20
			return nil
		}, "it would write more than"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newRegistryPull(t, true, "HTTP/1.1")
			p.fault = tc.fail(p)
			if note := p.note(t); !strings.Contains(note, tc.note) {
				t.Fatal(note)
			}
			if n := p.count(p.root); n != 1 {
				t.Fatalf("read %d times", n)
			}
		})
	}
}

// A registry that keeps failing is read three times, and the note says it failed rather than
// sending anyone to check its credentials.
func TestPullStopsReadingARegistryThatKeepsFailing(t *testing.T) {
	p := newRegistryPull(t, true, "HTTP/1.1")
	p.fault = func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	note := p.note(t)
	if !strings.Contains(note, "its registry failed or did not answer while the agent read it") || strings.Contains(note, "credential") {
		t.Fatal(note)
	}
	if n := p.count(p.root); n != 3 {
		t.Fatalf("read %d times", n)
	}
	if log := p.logged.String(); strings.Count(log, "answered 502") != 3 || strings.Count(log, "readsAgain=true") != 2 || strings.Count(log, "readsAgain=false") != 1 {
		t.Fatal(log)
	}
}

// The agent waits as long as a registry asks, and does not ask again when that is beyond its
// window, however long the registry asks for.
func TestPullHonoursRetryAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		reads  int
		waited time.Duration
	}{
		"within the window":    {"1", 2, time.Second},
		"beyond the window":    {"3600", 1, 0},
		"beyond any window":    {"4294967295", 1, 0},
		"until a distant date": {"Fri, 31 Dec 9999 23:59:59 GMT", 1, 0},
		"until a past date":    {"Fri, 31 Dec 1999 23:59:59 GMT", 2, 0},
		"that is not valid":    {"soon", 2, 0},
	} {
		t.Run(name, func(t *testing.T) {
			p := newRegistryPull(t, true, "HTTP/1.1")
			p.fault = func(w http.ResponseWriter, _ *http.Request, digest string, n int) bool {
				if digest != p.root || n > 1 {
					return false
				}
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
				return true
			}
			began := time.Now()
			if err := p.pull(t.Context()); (err == nil) != (tc.reads == 2) {
				t.Fatal(err)
			}
			if n := p.count(p.root); n != tc.reads {
				t.Fatalf("read %d times", n)
			}
			if waited := time.Since(began); waited < tc.waited {
				t.Fatal("waited only", waited)
			}
		})
	}
}

// Neither the end of the command's context nor its owner cancelling it waits out a retry.
func TestPullDoesNotReadAgainOnceCancelled(t *testing.T) {
	for _, by := range []string{"its context", "its owner", "its owner while the agent reads"} {
		t.Run(by, func(t *testing.T) {
			p := newRegistryPull(t, true, "HTTP/1.1")
			p.engine.registryRetry.backoff = []time.Duration{time.Hour, time.Hour}
			p.engine.registryRetry.window = 24 * time.Hour
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var cancelled atomic.Bool
			p.fault = func(w http.ResponseWriter, _ *http.Request, _ string, _ int) bool {
				w.WriteHeader(http.StatusServiceUnavailable)
				switch by {
				case "its context":
					time.AfterFunc(50*time.Millisecond, cancel)
				case "its owner":
					time.AfterFunc(50*time.Millisecond, func() { cancelled.Store(true) })
				default:
					cancelled.Store(true)
				}
				return true
			}
			done := make(chan error, 1)
			go func() { done <- p.engine.pullMeasuredImage(ctx, p.image, nil, cancelled.Load) }()
			select {
			case err := <-done:
				var refused *imagePullRefused
				if by == "its context" && !errors.Is(err, context.Canceled) || by != "its context" && !errors.As(err, &refused) {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("still waiting")
			}
			if n := p.count(p.root); n != 1 {
				t.Fatalf("read %d times", n)
			}
		})
	}
}

// A registry that stalls is read again, and one read after its window ended is the last.
func TestPullReadsAgainAfterAStall(t *testing.T) {
	registry := newTestRegistry()
	root := registry.image(gzipOf(t, tarOf(t, testFile{"app", 10})))
	released := make(chan struct{})
	registry.fault = func(_ http.ResponseWriter, _ *http.Request, d string, n int) bool {
		if d == root && n == 1 {
			<-released
		}
		return false
	}
	source := registry.source(t, nil)
	defer close(released)
	transport := source.(orasImage).repo.Client.(*auth.Client).Client.Transport.(retryAfterTransport).base.(*http.Transport)
	transport.ResponseHeaderTimeout = 100 * time.Millisecond
	read := func(ctx context.Context) error {
		_, err := planRegistryImage(ctx, source, digest.Digest(root), amd64Server, pullTestLimit, true, nil)
		return err
	}
	engine := &Engine{registryRetry: quickRegistryRetry()}
	if err := engine.readRegistry(t.Context(), "fixture/app", never, read); err != nil {
		t.Fatal(err)
	}
	if n := registry.count(root); n != 2 {
		t.Fatalf("read %d times", n)
	}
	engine.registryRetry.window = 50 * time.Millisecond
	registry.served = map[string]int{}
	if err := engine.readRegistry(t.Context(), "fixture/app", never, read); err == nil {
		t.Fatal("read again after its window")
	}
	if n := registry.count(root); n != 1 {
		t.Fatalf("read %d times", n)
	}
}

// The classic store's layers the agent had measured before a failure are not read again.
func TestPullReadsOnlyWhatFailedAgain(t *testing.T) {
	registry := newTestRegistry()
	first, second := gzipOf(t, tarOf(t, testFile{"first", 10})), gzipOf(t, tarOf(t, testFile{"second", 10}))
	root := registry.image(first, second)
	registry.fault = func(w http.ResponseWriter, _ *http.Request, d string, n int) bool {
		if d == sha(second) && n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	source := registry.source(t, nil)
	engine := &Engine{registryRetry: quickRegistryRetry()}
	if err := engine.readRegistry(t.Context(), "fixture/app", never, func(ctx context.Context) error {
		plan, err := planRegistryImage(ctx, source, digest.Digest(root), amd64Server, pullTestLimit, false, nil)
		if err != nil {
			return err
		}
		_, err = engine.measureRegistryImage(ctx, source, plan, pullTestLimit)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if a, b := registry.count(sha(first)), registry.count(sha(second)); a != 1 || b != 2 {
		t.Fatalf("read the first layer %d times and the second %d", a, b)
	}
}

// A failed read's log names no credential, no signed link and nothing the registry wrote.
func TestPullLogsNoCredential(t *testing.T) {
	const signature = "X-Amz-Signature=fixture-signature"
	for name, storage := range map[string]http.HandlerFunc{
		"an error": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"errors":[{"code":"UNAVAILABLE","message":"fixture-message"}]}`)
		},
		"the wrong length": func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "short") },
		"no answer":        func(w http.ResponseWriter, _ *http.Request) { hijacked(t, w, nil, false) },
		"a redirect loop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/loop?"+signature, http.StatusTemporaryRedirect)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := httptest.NewServer(storage)
			defer store.Close()
			p := newRegistryPull(t, true, "HTTP/1.1")
			p.credential = &pipeline.Credential{Username: "fixture-user", Password: "fixture-password"}
			p.fault = func(w http.ResponseWriter, req *http.Request, digest string, _ int) bool {
				if digest != p.layer {
					return false
				}
				http.Redirect(w, req, strings.Replace(store.URL, "http://", "http://fixture-user:fixture-password@", 1)+"/fixture-path?"+signature+"#fixture", http.StatusTemporaryRedirect)
				return true
			}
			_ = p.note(t)
			log := p.logged.String()
			if !strings.Contains(log, "could not read an image") {
				t.Fatal("not logged", log)
			}
			for _, secret := range []string{"Signature", "fixture-user", "fixture-password", "fixture-path", "fixture-message", "fixture-token"} {
				if strings.Contains(log, secret) {
					t.Fatal("logged "+secret, log)
				}
			}
		})
	}
}

func TestTransientRegistryFailures(t *testing.T) {
	answered := func(status int) error { return &errcode.ErrorResponse{StatusCode: status} }
	asked := func(err error) error { return &url.Error{Op: "Get", URL: "https://registry.example/v2/", Err: err} }
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"408":                           {answered(408), true},
		"429":                           {answered(429), true},
		"500":                           {answered(500), true},
		"503":                           {answered(503), true},
		"520":                           {answered(520), true},
		"400":                           {answered(400), false},
		"401":                           {answered(401), false},
		"403":                           {answered(403), false},
		"404":                           {answered(404), false},
		"416":                           {answered(416), false},
		"timeout":                       {asked(os.ErrDeadlineExceeded), true},
		"refused":                       {asked(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), true},
		"reset":                         {&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, true},
		"cut short":                     {io.ErrUnexpectedEOF, true},
		"closed before answering":       {asked(io.EOF), true},
		"name not found":                {asked(&net.DNSError{IsNotFound: true}), false},
		"name lookup failing for now":   {asked(&net.DNSError{IsTemporary: true}), true},
		"name lookup timing out":        {asked(&net.DNSError{IsTimeout: true}), true},
		"HTTP/2 stream refused":         {http2.StreamError{Code: http2.ErrCodeRefusedStream}, true},
		"HTTP/2 protocol error":         {http2.StreamError{Code: http2.ErrCodeProtocol}, false},
		"untrusted certificate":         {asked(errors.New("x509: certificate signed by unknown authority")), false},
		"content after its end":         {errLongerThanMeasured, false},
		"something else the agent read": {errors.New("invalid response"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := transientRegistryFailure(fmt.Errorf("reading: %w", tc.err)); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}

// The agent's own policy: three reads, about 2 and 8 seconds apart, none begun after 90 seconds.
func TestRegistryRetryPolicy(t *testing.T) {
	r := defaultRegistryRetry
	for range 1000 {
		for attempt, base := range map[int]time.Duration{1: 2 * time.Second, 2: 8 * time.Second} {
			wait, again := r.wait(attempt, 0, 0)
			if !again || wait < base*3/4 || wait > base*5/4 {
				t.Fatal(attempt, wait, again)
			}
		}
	}
	for name, tc := range map[string]struct {
		attempt        int
		elapsed, asked time.Duration
		want           bool
	}{
		"after the last read":                 {3, 0, 0, false},
		"asked to wait the window":            {1, 0, 90 * time.Second, true},
		"asked to wait beyond the window":     {1, 0, 91 * time.Second, false},
		"asked to wait past what is left":     {1, 60 * time.Second, 31 * time.Second, false},
		"asked to wait longer than it counts": {1, time.Second, 1<<63 - 1, false},
		"after the window":                    {1, 2 * time.Minute, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			if wait, again := r.wait(tc.attempt, tc.elapsed, tc.asked); again != tc.want || again && wait < tc.asked {
				t.Fatal(wait, again)
			}
		})
	}
}
