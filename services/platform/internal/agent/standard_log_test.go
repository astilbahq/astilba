package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// agentLog makes the agent's log, as the agent service sets it, write to a buffer until the test
// ends.
func agentLog(t *testing.T) *syncBuffer {
	t.Helper()
	logged := &syncBuffer{}
	previous, writer, flags, prefix := slog.Default(), log.Writer(), log.Flags(), log.Prefix()
	SetLogger(slog.New(slog.NewTextHandler(logged, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	return logged
}

// syncBuffer is a buffer that net/http's connection goroutines and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A registry that sends an answer the agent did not ask for, on a connection it keeps for later,
// can't put what it sent into the agent's log.
func TestRegistryCannotWriteToTheAgentLog(t *testing.T) {
	logged := agentLog(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	const manifest = `{"schemaVersion":2}`
	const forged = "HTTP/1.1 200 OK\r\n\x1b[2J\x1b[31mfixture-injected\r\ntime=2026-01-01 level=ERROR msg=fixture-forged\n"
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\nDocker-Content-Digest: %s\r\nContent-Length: %d\r\n\r\n%s", ocispec.MediaTypeImageManifest, sha([]byte(manifest)), len(manifest), manifest)
		// Once the agent has read the answer and keeps the connection idle.
		time.Sleep(200 * time.Millisecond)
		_, _ = conn.Write([]byte(forged))
		time.Sleep(time.Second)
	}()
	source, err := newRegistryImage(listener.Addr().String(), "fixture/app", true, true, nil, http.ProxyFromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	// An answer the agent reads whole leaves the connection idle; a failed one would close it.
	if _, _, err = source.manifest(t.Context(), digest.Digest(sha([]byte(manifest))), imageMetadataBytes); err != nil {
		t.Fatal(err)
	}
	text := ""
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.Contains(text, "did not ask for"); time.Sleep(20 * time.Millisecond) {
		text = logged.String()
	}
	if !strings.Contains(text, "level=WARN") || !strings.Contains(text, "a server sent the agent an answer it did not ask for") {
		t.Fatal("not logged", text)
	}
	for _, part := range []string{"fixture-injected", "fixture-forged", "\x1b", "HTTP/1.1 200"} {
		if strings.Contains(text, part) {
			t.Fatalf("logged %q: %s", part, text)
		}
	}
}

// Anything else the standard library logs is one escaped line of bounded length.
func TestStandardLogIsEscapedAndBounded(t *testing.T) {
	logged := agentLog(t)
	log.Print("net/http: invalid byte \x1b[31m\u202e\xff in fixture\nlevel=ERROR msg=fixture-forged " + strings.Repeat("x", 4096))
	text := logged.String()
	if strings.Count(text, "\n") != 1 || !strings.Contains(text, "level=WARN") || !strings.Contains(text, `msg="the Go standard library logged an event"`) {
		t.Fatal(text)
	}
	if strings.ContainsAny(text, "\x1b\u202e") || strings.Contains(text, "\xff") || !strings.Contains(text, `\\x1b[31m\\u202e in fixture\\nlevel=ERROR`) {
		t.Fatal("not escaped:", text)
	}
	if len(text) > 2*standardLogBytes {
		t.Fatal("not bounded:", len(text))
	}
}
