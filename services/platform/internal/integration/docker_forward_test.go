package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// dockerForward reaches a port inside a container without publishing it, the way
// `kubectl port-forward` goes through the API server. It listens on a loopback port the
// kernel gives this process and carries each connection over the Docker API, running
// ./relay in the container. A published port goes through the VM's port forwarder to the
// same number on the host, where another VM's port can answer instead. This port
// belongs to this process and only ever reaches the container named.
// scripts/docker-forward.mjs does the same for the browser fixture.
type dockerForward struct {
	listener net.Listener
	docker   *client.Client
	// The container that runs the relay, and what the relay connects to from there.
	container string
	target    []string
	mu        sync.Mutex
	open      map[net.Conn]struct{}
	closed    bool
}

// relayPath is where the relay is copied in each container that runs it.
const relayPath = "/tmp/astilba-relay"

// relayBuild is ./relay built for the daemon's architecture, once per process.
var relayBuild = sync.OnceValues(func() ([]byte, error) {
	directory, err := os.MkdirTemp("", "astilba-relay-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		return nil, err
	}
	defer docker.Close()
	version, err := docker.ServerVersion(context.Background(), client.ServerVersionOptions{})
	if err != nil {
		return nil, err
	}
	binary := filepath.Join(directory, "relay")
	// The tests run in this package's directory.
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./relay")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+version.Arch)
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build the relay: %w: %s", err, out)
	}
	return os.ReadFile(binary)
})

func startDockerForward(docker *client.Client, container, address, port string) (*dockerForward, error) {
	relay, err := relayBuild()
	if err != nil {
		return nil, err
	}
	var archive bytes.Buffer
	files := tar.NewWriter(&archive)
	if err = files.WriteHeader(&tar.Header{Name: filepath.Base(relayPath), Mode: 0o755, Size: int64(len(relay))}); err != nil {
		return nil, err
	}
	if _, err = files.Write(relay); err != nil {
		return nil, err
	}
	if err = files.Close(); err != nil {
		return nil, err
	}
	if _, err = docker.CopyToContainer(context.Background(), container, client.CopyToContainerOptions{DestinationPath: filepath.Dir(relayPath), Content: &archive}); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &dockerForward{listener: listener, docker: docker, container: container, target: []string{relayPath, net.JoinHostPort(address, port)}, open: map[net.Conn]struct{}{}}
	go f.serve()
	return f, nil
}

// Addr is the host and port to dial instead of the container's.
func (f *dockerForward) Addr() string { return f.listener.Addr().String() }

// Close stops accepting and ends the connections still open, as removing the container would.
func (f *dockerForward) Close() {
	f.mu.Lock()
	f.closed = true
	for c := range f.open {
		c.Close()
	}
	f.mu.Unlock()
	f.listener.Close()
}

func (f *dockerForward) serve() {
	for {
		c, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			c.Close()
			return
		}
		f.open[c] = struct{}{}
		f.mu.Unlock()
		go f.carry(c)
	}
}

// carry connects one client through a new exec. A failed exec closes the client's
// connection, which its driver reports as the server closing it.
func (f *dockerForward) carry(c net.Conn) {
	defer func() {
		f.mu.Lock()
		delete(f.open, c)
		f.mu.Unlock()
		c.Close()
	}()
	ctx := context.Background()
	created, err := f.docker.ExecCreate(ctx, f.container, client.ExecCreateOptions{AttachStdin: true, AttachStdout: true, Cmd: f.target})
	if err != nil {
		return
	}
	attached, err := f.docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return
	}
	defer attached.Close()
	go func() {
		// The relay passes on the end of the client's input, and ends when the server does.
		_, _ = io.Copy(attached.Conn, c)
		_ = attached.CloseWrite()
	}()
	// Without a terminal the daemon frames the output; only stdout is attached.
	_, _ = stdcopy.StdCopy(c, io.Discard, attached.Reader)
}
