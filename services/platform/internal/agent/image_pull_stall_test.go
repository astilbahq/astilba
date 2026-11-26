package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	bolt "go.etcd.io/bbolt"
)

// A read that waits idle for its source ends the answer as stalled, and a reader that only asks
// for more slowly never does.
func TestIdleAnswerEndsOnlyAWaitingRead(t *testing.T) {
	const idle = 100 * time.Millisecond
	ctx, cancel := context.WithCancelCause(t.Context())
	reader, writer := io.Pipe()
	context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	answer := newIdleAnswer(reader, idle, cancel)
	go func() {
		_, _ = writer.Write([]byte("abc"))
	}()
	buf := make([]byte, 1)
	for range 3 {
		if _, err := answer.Read(buf); err != nil {
			t.Fatal(err)
		}
		// Slower than idle between reads, as Docker can be while it writes.
		time.Sleep(3 * idle)
	}
	began := time.Now()
	if _, err := answer.Read(buf); !errors.Is(err, errAnswerStalled) {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited < idle || waited > 10*idle {
		t.Fatal("waited", waited)
	}
	if !errors.Is(context.Cause(ctx), errAnswerStalled) {
		t.Fatal("the request was not ended", context.Cause(ctx))
	}
	if !transientRegistryFailure(&sourceFailure{err: errAnswerStalled}) {
		t.Fatal("a stalled answer is not read again")
	}
	if err := answer.Close(); err != nil {
		t.Fatal(err)
	}
}

// stallingRegistry serves registry over TLS with proto, as a source whose answers stall after idle.
// The registry stops sending its first answer for stalled halfway through, until the test ends.
func stallingRegistry(t *testing.T, registry *testRegistry, proto string, stalled string, idle time.Duration) registryImage {
	t.Helper()
	released := make(chan struct{})
	registry.fault = func(w http.ResponseWriter, req *http.Request, d string, n int) bool {
		if req.Proto != proto {
			t.Error("the registry was read over", req.Proto)
		}
		if d != stalled || n > 1 || req.Method != http.MethodGet {
			return false
		}
		content := registry.blobs[d]
		if m, ok := registry.manifests[d]; ok {
			content = []byte(m[1])
			w.Header().Set("Content-Type", m[0])
			w.Header().Set("Docker-Content-Digest", d)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content[:len(content)/2])
		_ = http.NewResponseController(w).Flush()
		select {
		case <-released:
		case <-req.Context().Done():
		}
		return true
	}
	server := httptest.NewUnstartedServer(registry)
	server.EnableHTTP2 = proto == "HTTP/2.0"
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(released) })
	source, err := newRegistryImage(strings.TrimPrefix(server.URL, "https://"), "fixture/app", true, false, nil, http.ProxyFromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	source.(orasImage).transport.idle = idle
	return source
}

// A registry that stops sending a manifest or a blob partway is read again once it has been idle
// for the transport's idle time, long before any outer deadline.
func TestPullReadsAgainAfterAnAnswerStalls(t *testing.T) {
	for _, proto := range []string{"HTTP/1.1", "HTTP/2.0"} {
		for _, what := range []string{"manifest", "layer"} {
			t.Run(proto+"/"+what, func(t *testing.T) {
				logged := captureTrail(t)
				registry := newTestRegistry()
				layer := gzipOf(t, tarOf(t, testFile{"app", 64 << 10}))
				root := registry.image(layer)
				stalled := sha(layer)
				if what == "manifest" {
					stalled = root
				}
				source := stallingRegistry(t, registry, proto, stalled, 200*time.Millisecond)
				began := time.Now()
				if err := readSources(t, []registryImage{source}, root); err != nil {
					t.Fatal(err)
				}
				if waited := time.Since(began); waited > 10*time.Second {
					t.Fatal("waited", waited)
				}
				if n := registry.count(stalled); n != 2 {
					t.Fatalf("read %d times", n)
				}
				if log := logged.String(); !strings.Contains(log, `cause="stopped sending its answer"`) || !strings.Contains(log, "and will read it again") {
					t.Fatal(log)
				}
			})
		}
	}
}

// The command's owner cancelling it ends a read its registry is still sending, without waiting
// for the registry or the transport's idle time.
func TestPullStopsReadingOnceCancelled(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 64 << 10}))
	root := registry.image(layer)
	source := stallingRegistry(t, registry, "HTTP/1.1", sha(layer), time.Hour)
	var cancelled atomic.Bool
	time.AfterFunc(300*time.Millisecond, func() { cancelled.Store(true) })
	engine := &Engine{registryRetry: quickRegistryRetry()}
	done := make(chan error, 1)
	go func() {
		done <- engine.readRegistry(t.Context(), "fixture/app", cancelled.Load, func(ctx context.Context) error {
			plan, err := planRegistryImage(ctx, source, digest.Digest(root), amd64Server, pullTestLimit, false, nil)
			if err != nil {
				return err
			}
			_, err = engine.measureRegistryImage(ctx, source, plan, pullTestLimit)
			return err
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errPullCancelled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still reading")
	}
	if n := registry.count(sha(layer)); n != 1 {
		t.Fatalf("read %d times", n)
	}
}

// A command whose image's registry has stopped sending it doesn't hold work that may run beside a
// command, and the command goes on only once that work has finished.
func TestStalledPullDoesNotHoldTheEngine(t *testing.T) {
	p := newRegistryPull(t, true, "HTTP/1.1")
	p.engine.registryIdle = time.Hour
	reading, released := make(chan struct{}), make(chan struct{})
	p.fault = func(w http.ResponseWriter, req *http.Request, digest string, _ int) bool {
		if digest != p.layer {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(p.blobs[p.layer])))
		_, _ = w.Write(p.blobs[p.layer][:1<<10])
		_ = http.NewResponseController(w).Flush()
		close(reading)
		select {
		case <-released:
		case <-req.Context().Done():
		}
		return true
	}
	journal, plan := testVisualJournal(t)
	p.engine.Journal = journal
	command := api.ExecutionCommand{Id: uuid.New()}
	if err := journal.db.Update(func(tx *bolt.Tx) error {
		return journal.put(tx, "operations", command.Id.String(), Record{Command: command})
	}); err != nil {
		t.Fatal(err)
	}
	plan.Images = map[string]string{"app": p.image}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The command holds the engine, as Execute does.
	p.engine.mu.Lock()
	defer p.engine.mu.Unlock()
	pulled := make(chan error, 1)
	go func() { pulled <- p.engine.pullVisualImages(ctx, command, deployment.Payload{Plan: plan}) }()
	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the layer was not read")
	}
	stepped := make(chan struct{})
	var after atomic.Bool
	go func() {
		_ = p.engine.betweenCommands(t.Context(), func(context.Context) error {
			close(stepped)
			// The registry sends the rest, and the pull ends, while this work runs.
			close(released)
			cancel()
			time.Sleep(200 * time.Millisecond)
			after.Store(true)
			return nil
		})
	}()
	select {
	case <-stepped:
	case <-time.After(10 * time.Second):
		t.Fatal("work beside the command waited for the stalled pull")
	}
	select {
	case <-pulled:
		if !after.Load() {
			t.Fatal("the command went on while work beside it ran")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pull did not end")
	}
	if err := p.engine.betweenCommands(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, errEngineBusy) {
		t.Fatal("work beside the command ran after it took the engine back", err)
	}
}
