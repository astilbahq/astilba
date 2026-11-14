package agent

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// InsecureKeyStore seals with a key that lives only in this process, for tests and development.
// Like a real machine's store it refuses what another store sealed, or what was sealed under another
// name, but nothing protects it, so it
// reports its key as unsealed. The agent's commands never use it.
func InsecureKeyStore() KeyStore {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return insecureStore{aead}
}

type insecureStore struct{ aead cipher.AEAD }

func (insecureStore) Protection() api.AgentCapabilitiesKeyProtection { return api.Unsealed }

func (s insecureStore) Seal(_ context.Context, name string, plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(name)), nil
}

func (s insecureStore) Unseal(_ context.Context, name string, sealed []byte) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("sealed by another store")
	}
	plain, err := s.aead.Open(nil, sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():], []byte(name))
	if err != nil {
		return nil, errors.New("sealed by another store")
	}
	return plain, nil
}
