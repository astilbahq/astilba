// Command relay carries one connection between its standard streams and the address
// it is given. The test harnesses run it inside a container with `docker exec`, so a
// test reaches the container's port without publishing it; see dockerForward in
// internal/integration and scripts/docker-forward.mjs. It does what `nc` would, except
// that it ends when the server closes, as well as passing on the end of its input.
// BusyBox nc waits for its input to end, which holds a PostgreSQL cancel request open
// until the client gives up.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relay HOST:PORT")
		os.Exit(2)
	}
	conn, err := net.DialTimeout("tcp", os.Args[1], 10*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		// The client has finished sending; the server still answers what it has.
		_ = conn.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
}
