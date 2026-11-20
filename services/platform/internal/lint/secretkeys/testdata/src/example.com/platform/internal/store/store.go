package store

import "example.com/platform/internal/cryptobox"

type Store struct {
	Keys *cryptobox.Keyring // want `Keys, a keyring, gives keys` `cryptobox.Keyring gives keys`
}
