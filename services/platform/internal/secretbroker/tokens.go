package secretbroker

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

// TokenKind is a self-issued token sealed under the resident token key.
type TokenKind string

const (
	DeviceToken TokenKind = "login-device:v1"
	ResetMail   TokenKind = "reset-mail:v1"
)

// Tokens seals and opens short-lived tokens management issues to itself. They are not stored
// customer values, and needing the key service on every sign-in page would add nothing to the
// record, so this key stays resident: its one record is made when it is loaded.
type Tokens struct {
	ring *cryptobox.Keyring
}

func (b *Broker) Tokens(ctx context.Context) (*Tokens, error) {
	if err := b.record(ctx, uuid.New(), Request{Purpose: Token, Principal: System("token-keys")}); err != nil {
		return nil, err
	}
	return &Tokens{b.backend.Keyring()}, nil
}

func (t *Tokens) Seal(kind TokenKind, plaintext []byte) ([]byte, error) {
	return t.ring.Seal(plaintext, string(kind))
}

func (t *Tokens) Open(kind TokenKind, ciphertext []byte) ([]byte, error) {
	return t.ring.Open(ciphertext, string(kind))
}
