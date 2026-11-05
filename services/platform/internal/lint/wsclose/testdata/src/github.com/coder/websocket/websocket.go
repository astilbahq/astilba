package websocket

type StatusCode int

type Conn struct{}

func (*Conn) Close(StatusCode, string) error { return nil }
func (*Conn) CloseNow() error                { return nil }
