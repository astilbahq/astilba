package secretbroker

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

// TokenKind is a self-issued token sealed under the resident token key. Only the kinds below
// exist: a caller cannot name another identity and open a stored value with this key.
type TokenKind struct{ identity string }

var (
	DeviceToken = TokenKind{"login-device:v1"}
	ResetMail   = TokenKind{"reset-mail:v1"}
)

// Tokens seals and opens short-lived tokens management issues to itself. They are not stored
// customer values, and needing the key service on every sign-in page would add nothing to the
// record, so this key stays resident: its one record is made when it is loaded.
type Tokens struct {
	ring *cryptobox.Keyring
}

func (b *Broker) Tokens(ctx context.Context) (*Tokens, error) {
	id := uuid.New()
	if err := b.record(ctx, id, Request{Purpose: Token, Principal: System("token-keys")}); err != nil {
		return nil, err
	}
	// The key is now resident for this process's life, which is the record's whole story.
	if err := b.outcome(ctx, id, Released); err != nil {
		return nil, err
	}
	return &Tokens{b.backend.Keyring()}, nil
}

func (t *Tokens) Seal(kind TokenKind, plaintext []byte) ([]byte, error) {
	if kind.identity == "" {
		return nil, ErrNotAllowed
	}
	return t.ring.Seal(plaintext, kind.identity)
}

func (t *Tokens) Open(kind TokenKind, ciphertext []byte) ([]byte, error) {
	if kind.identity == "" {
		return nil, ErrNotAllowed
	}
	return t.ring.Open(ciphertext, kind.identity)
}
