package integration

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// dockerForward reaches a port inside a container without publishing it, the way
// `kubectl port-forward` goes through the API server. It listens on a loopback port the
// kernel gives this process and carries each connection over the Docker API, running
// `nc` in the container. A published port goes through the VM's port forwarder to the
// same number on the host, where another VM's port can answer instead. This port
// belongs to this process and only ever reaches the container named.
// scripts/docker-forward.mjs does the same for the browser fixture.
type dockerForward struct {
	listener net.Listener
	docker   *client.Client
	// The container that runs nc, and the address and port nc connects to from there.
	container string
	target    []string
	mu        sync.Mutex
	open      map[net.Conn]struct{}
	closed    bool
}

func startDockerForward(docker *client.Client, container, address, port string) (*dockerForward, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &dockerForward{listener: listener, docker: docker, container: container, target: []string{"nc", address, port}, open: map[net.Conn]struct{}{}}
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
		// nc exits when its input ends, which ends its output below.
		_, _ = io.Copy(attached.Conn, c)
		_ = attached.CloseWrite()
	}()
	// Without a terminal the daemon frames the output; only stdout is attached.
	_, _ = stdcopy.StdCopy(c, io.Discard, attached.Reader)
}
