package execution

import (
	"errors"

	"example.com/platform/internal/cryptobox"
	"example.com/platform/internal/custody" // want `the custody package gives keys`
)

type Service struct {
	keys *cryptobox.Keyring // want `cryptobox.Keyring gives keys`
}

func (s *Service) open(v []byte) ([]byte, error) {
	return s.keys.Open(v, "identity") // want `Keyring.Open gives keys`
}

func load() {
	_, _ = cryptobox.Load("path") // want `cryptobox.Load gives keys`
	_ = custody.NewFile(nil)      // want `custody.NewFile gives keys`
}

// Errors and other names that hold no key are anyone's.
func tooLarge(err error) bool { return errors.Is(err, cryptobox.ErrTooLarge) }
