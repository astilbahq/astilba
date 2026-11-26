package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	bolt "go.etcd.io/bbolt"
)

// An answer ends as stalled once its source sends too little in a window of waiting, whether it
// stops or sends a byte now and then, and not while it keeps up or the reader is slow to ask.
func TestSlowAnswer(t *testing.T) {
	const window = 100 * time.Millisecond
	progress := registryProgress{window: window, least: 16}
	for name, tc := range map[string]struct {
		// send is what the source sends, before it stops.
		send    func(w *io.PipeWriter)
		read    func(answer io.Reader) error
		stalled bool
	}{
		"a source that stops": {
			send: func(w *io.PipeWriter) { _, _ = w.Write([]byte("abc")) },
			read: func(answer io.Reader) error {
				buf := make([]byte, 1)
				for range 3 {
					if _, err := answer.Read(buf); err != nil {
						return err
					}
					// Slower than the window between reads, as Docker can be while it writes.
					time.Sleep(3 * window)
				}
				_, err := answer.Read(buf)
				return err
			},
			stalled: true,
		},
		"a source that drips": {
			send: func(w *io.PipeWriter) {
				for range 200 {
					if _, err := w.Write([]byte("x")); err != nil {
						return
					}
					time.Sleep(window / 5)
				}
			},
			read: func(answer io.Reader) error {
				_, err := io.Copy(io.Discard, answer)
				return err
			},
			stalled: true,
		},
		"a source that keeps up": {
			send: func(w *io.PipeWriter) {
				// Ten times what each window asks, so a late wake-up doesn't fall short.
				for range 50 {
					_, _ = w.Write(make([]byte, 16))
					time.Sleep(window / 10)
				}
				_ = w.Close()
			},
			read: func(answer io.Reader) error {
				_, err := io.Copy(io.Discard, answer)
				return err
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			reader, writer := io.Pipe()
			context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
			answer := newSlowAnswer(reader, progress, cancel)
			go tc.send(writer)
			began := time.Now()
			err := tc.read(answer)
			if !tc.stalled {
				if err != nil || context.Cause(ctx) != nil {
					t.Fatal(err, context.Cause(ctx))
				}
				return
			}
			if !errors.Is(err, errAnswerStalled) || !errors.Is(context.Cause(ctx), errAnswerStalled) {
				t.Fatal("not stalled", err, context.Cause(ctx))
			}
			// Within two windows of waiting, long before the source would have finished.
			if waited := time.Since(began); waited > 10*window+3*3*window {
				t.Fatal("waited", waited)
			}
			if _, err = answer.Read(make([]byte, 1)); !errors.Is(err, errAnswerStalled) {
				t.Fatal("read on after stalling", err)
			}
			if err = answer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if !transientRegistryFailure(&sourceFailure{err: errAnswerStalled}) {
		t.Fatal("a stalled answer is not read again")
	}
}

// stallingRegistry serves registry over TLS with proto, as a source that must send as progress
// asks. The registry sends half of its first answer for stalled, then nothing more until the test
// ends or, if it drips, the rest a byte at a time.
func stallingRegistry(t *testing.T, registry *testRegistry, proto, stalled string, drips bool, progress registryProgress) registryImage {
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
		// A registry that drips ends its answer, if the agent waits for all of it.
		for i := len(content) / 2; drips && i < len(content); i++ {
			select {
			case <-released:
				return true
			case <-req.Context().Done():
				return true
			case <-time.After(20 * time.Millisecond):
			}
			_, _ = w.Write(content[i : i+1])
			_ = http.NewResponseController(w).Flush()
		}
		if drips {
			return true
		}
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
	source.(orasImage).transport.progress = progress
	return source
}

// A registry that stops sending a manifest or a blob partway, or sends the rest a byte at a time,
// is read again once it has sent too little for a window, long before any outer deadline.
func TestPullReadsAgainAfterAnAnswerStalls(t *testing.T) {
	for _, proto := range []string{"HTTP/1.1", "HTTP/2.0"} {
		for _, what := range []string{"manifest", "layer"} {
			for _, how := range []string{"stops", "drips"} {
				t.Run(proto+"/"+what+"/"+how, func(t *testing.T) {
					logged := captureTrail(t)
					registry := newTestRegistry()
					layer := gzipOf(t, tarOf(t, testFile{"app", 64 << 10}))
					root := registry.image(layer)
					stalled := sha(layer)
					if what == "manifest" {
						stalled = root
					}
					source := stallingRegistry(t, registry, proto, stalled, how == "drips", registryProgress{window: 500 * time.Millisecond, least: 16 << 10})
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
}

// The command's owner cancelling it ends a read its registry is still sending, without waiting
// for the registry or the transport's window.
func TestPullStopsReadingOnceCancelled(t *testing.T) {
	registry := newTestRegistry()
	layer := gzipOf(t, tarOf(t, testFile{"app", 64 << 10}))
	root := registry.image(layer)
	source := stallingRegistry(t, registry, "HTTP/1.1", sha(layer), false, registryProgress{window: time.Hour, least: 1})
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

// A command whose image's registry has stopped sending it lends the engine: private links are
// restored and other work beside a command runs, and schedule ticks and route conversion skip
// their turn rather than wait. Once the registry sends the rest, the command goes on only after
// the work beside it has finished, its image read whole, and takes the engine back.
func TestStalledPullLendsTheEngine(t *testing.T) {
	p := newRegistryPull(t, true, "HTTP/1.1")
	p.engine.registryProgress = &registryProgress{window: time.Hour, least: 1}
	reading, released := make(chan struct{}), make(chan struct{})
	p.fault = func(w http.ResponseWriter, req *http.Request, digest string, _ int) bool {
		if digest != p.layer {
			return false
		}
		blob := p.blobs[p.layer]
		w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
		_, _ = w.Write(blob[:1<<10])
		_ = http.NewResponseController(w).Flush()
		close(reading)
		select {
		case <-released:
			_, _ = w.Write(blob[1<<10:])
		case <-req.Context().Done():
		}
		return true
	}
	journal, plan := testVisualJournal(t)
	p.engine.Journal = journal
	// A host with private links, none of them missing.
	if err := os.Mkdir(filepath.Join(journal.dir, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	command := api.ExecutionCommand{Id: uuid.New()}
	if err := journal.db.Update(func(tx *bolt.Tx) error {
		return journal.put(tx, "operations", command.Id.String(), Record{Command: command})
	}); err != nil {
		t.Fatal(err)
	}
	plan.Images = map[string]string{"app": p.image}
	// The command holds the engine, as Execute does.
	p.engine.mu.Lock()
	held := true
	defer func() {
		if held {
			p.engine.mu.Unlock()
		}
	}()
	pulled := make(chan error, 1)
	go func() { pulled <- p.engine.pullVisualImages(t.Context(), command, deployment.Payload{Plan: plan}) }()
	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the layer was not read")
	}
	if err := p.engine.restorePrivateLinks(t.Context(), func(hostnetwork.Snapshot) bool { return false }); err != nil {
		t.Fatal("private links were not restored during the pull:", err)
	}
	for name, turn := range map[string]func(context.Context) error{"a schedule tick": p.engine.TickSchedules, "route conversion": p.engine.QualifyRouteBackends} {
		skipped := make(chan error, 1)
		go func() { skipped <- turn(t.Context()) }()
		select {
		case err := <-skipped:
			if err != nil {
				t.Fatal(name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal(name, "waited for the pull")
		}
	}
	stepped := make(chan struct{})
	var after atomic.Bool
	go func() {
		_ = p.engine.betweenCommands(t.Context(), func(context.Context) error {
			close(stepped)
			// The registry sends the rest, and the pull ends, while this work runs.
			close(released)
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
	case err := <-pulled:
		if err != nil {
			t.Fatal(err)
		}
		if !after.Load() {
			t.Fatal("the command went on while work beside it ran")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pull did not end")
	}
	if n := p.count(p.layer); n != 1 {
		t.Fatalf("read the layer %d times", n)
	}
	// The command has the engine back.
	if err := p.engine.betweenCommands(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, errEngineBusy) {
		t.Fatal("work beside the command ran after it took the engine back", err)
	}
	ticked := make(chan error, 1)
	go func() { ticked <- p.engine.TickSchedules(t.Context()) }()
	select {
	case <-ticked:
		t.Fatal("a schedule tick ran inside the command")
	case <-time.After(200 * time.Millisecond):
	}
	held = false
	p.engine.mu.Unlock()
	select {
	case err := <-ticked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a schedule tick waited after the command ended")
	}
}
