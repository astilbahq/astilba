package terminal

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// closeLimit is beyond the library's limits for a peer that sends nothing.
const closeLimit = 10 * time.Second

// Close ends a stream with a close handshake, which reads any input still queued. The
// library has no time limit while it discards the payload of a frame, so a peer that stops
// inside one would hold the handshake for as long as it stays connected. Only CloseRead can
// then drop the connection: CloseNow waits for the Close in progress.
func Close(conn *websocket.Conn, status websocket.StatusCode, reason string) error {
	limit := time.AfterFunc(closeLimit, func() {
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		conn.CloseRead(ended)
	})
	defer limit.Stop()
	return conn.Close(status, reason)
}
