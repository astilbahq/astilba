package secretbroker

import (
	"example.com/platform/internal/cryptobox"
	"example.com/platform/internal/custody"
)

type Broker struct {
	ring *cryptobox.Keyring
	file *custody.File
}

func (b *Broker) Open(v []byte) ([]byte, error) { return b.ring.Open(v, "identity") }
