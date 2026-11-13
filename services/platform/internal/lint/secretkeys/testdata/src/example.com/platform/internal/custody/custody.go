package custody

import "example.com/platform/internal/cryptobox"

type File struct{ ring *cryptobox.Keyring }

func NewFile(ring *cryptobox.Keyring) *File { return &File{ring} }
