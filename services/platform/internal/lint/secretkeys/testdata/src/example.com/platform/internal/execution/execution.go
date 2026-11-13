package execution

import (
	"errors"

	"example.com/platform/internal/cryptobox"
	"example.com/platform/internal/custody" // want `the custody package gives keys`
	"example.com/platform/internal/store"
)

type Service struct {
	keys  *cryptobox.Keyring // want `keys, a keyring, gives keys` `cryptobox.Keyring gives keys`
	store *store.Store
}

func (s *Service) open(v []byte) ([]byte, error) {
	return s.keys.Open(v, "identity") // want `keys, a keyring, gives keys` `cryptobox.Open gives keys`
}

func load() {
	_, _ = cryptobox.Load("path") // want `cryptobox.Load gives keys`
	_ = custody.NewFile(nil)      // want `custody.NewFile gives keys`
}

// A keyring reached through another package's field is still a keyring.
func (s *Service) throughStore(v []byte) ([]byte, error) {
	var opener interface {
		Open([]byte, string) ([]byte, error)
	} = s.store.Keys // want `Keys, a keyring, gives keys`
	return opener.Open(v, "identity")
}

// So is a call to the transitional functions from anywhere new.
func (s *Service) newCaller(v []byte) ([]byte, error) {
	return s.legacy(v) // want `legacy, which opens values without a record, gives keys`
}

// Errors hold no key and are anyone's.
func tooLarge(err error) bool { return errors.Is(err, cryptobox.ErrTooLarge) }

// Any number of calls away is still a way to keys.
func (s *Service) throughWrapper(v []byte) ([]byte, error) {
	return s.wrapper(v) // want `wrapper, which opens values without a record, gives keys`
}

// Making the transitional type outside the listed files is reported, though the call goes
// through an interface.
func (s *Service) throughInterface(v []byte) ([]byte, error) {
	var o opener = legacyOpener{s} // want `legacyOpener, which opens values without a record, gives keys`
	return o.open(v)
}
