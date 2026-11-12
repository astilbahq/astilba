package secretbroker

type Broker struct{}
type Session struct{}

func (*Broker) Begin() (*Session, error)        { return nil, nil }
func (*Session) Open(b []byte) ([]byte, error) { return b, nil }
