package pgxpool

type Pool struct{}

func (*Pool) Acquire() (*Conn, error) { return &Conn{}, nil }
func (*Pool) Exec() error             { return nil }

type Conn struct{}

func (*Conn) Exec() error { return nil }
func (*Conn) Release()    {}
