// Package randtoken makes random, URL-safe tokens. It holds no keys, so any package may use
// it; opening or sealing stored values goes through the secret broker instead.
package randtoken

import (
	"crypto/rand"
	"encoding/base64"
	"io"
)

// New returns 32 random bytes, encoded as unpadded base64url (43 characters).
func New() (string, error) {
	var data [32]byte
	if _, err := io.ReadFull(rand.Reader, data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
