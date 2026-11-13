package cryptobox

import "errors"

var ErrTooLarge = errors.New("too large")

type Keyring struct{}

func New(string) (*Keyring, error)                       { return &Keyring{}, nil }
func Load(string) (*Keyring, error)                      { return &Keyring{}, nil }
func Parse([]byte) (*Keyring, error)                     { return &Keyring{}, nil }
func (*Keyring) Open(b []byte, _ string) ([]byte, error) { return b, nil }
func (*Keyring) Seal(b []byte, _ string) ([]byte, error) { return b, nil }
