package relay

import "github.com/coder/websocket"

type stream struct{}

func (stream) Close(websocket.StatusCode, string) error { return nil }

func closes(conn *websocket.Conn, other stream) {
	conn.Close(1000, "")        // want `a peer that stalls inside a frame holds this close forever`
	defer conn.Close(1008, "x") // want `a peer that stalls inside a frame`
	later := conn.Close         // want `a peer that stalls inside a frame`
	_ = later
	conn.CloseNow()
	other.Close(1000, "")
}
