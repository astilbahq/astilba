package terminal

import (
	"context"
	"errors"
	"time"

	"github.com/coder/websocket"
)

// closeLimit is beyond the library's limits for a peer that sends nothing.
const closeLimit = 10 * time.Second

// maxCloseReason is the most text a close frame carries.
const maxCloseReason = 123

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
	return conn.Close(status, CloseReason(reason))
}

// CloseReason fits a reason into a close frame. The library refuses a longer one, and then
// sends an internal error with no reason at all.
func CloseReason(reason string) string {
	if len(reason) > maxCloseReason {
		return reason[:maxCloseReason]
	}
	return reason
}

// Ending reports whether a read failed because the peer closed the stream, which it does
// on purpose, and the reason it gave. Any other failure is a lost stream. A reason that is
// not printable ASCII is dropped, because the agent writes it to the host's journal.
func Ending(err error) (reason string, closed bool) {
	var ended websocket.CloseError
	if !errors.As(err, &ended) {
		return "", false
	}
	for _, c := range []byte(ended.Reason) {
		if c < ' ' || c > '~' {
			return "", true
		}
	}
	return ended.Reason, true
}
