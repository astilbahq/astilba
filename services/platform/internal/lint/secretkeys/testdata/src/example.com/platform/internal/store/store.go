package store

import "example.com/platform/internal/cryptobox"

// A pending file: the keyring field is declared here.
type Store struct{ Keys *cryptobox.Keyring }
