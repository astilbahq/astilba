package terminal

import "github.com/coder/websocket"

// The package that owns the limited close may use the library call.
func Close(conn *websocket.Conn, status websocket.StatusCode, reason string) error {
	return conn.Close(status, reason)
}
