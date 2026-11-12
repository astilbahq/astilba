package execution

import (
	"context"
	"encoding/json/v2"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// The agent delivery path still opens values with the installation keyring, without a record.
// It moves to the broker with release records (PLAT-196), and this file then goes. The
// secretkeys analyzer allows nothing new.

func (s *Service) legacyOpenIntegration(ws, id uuid.UUID, kind string, raw []byte, value any) error {
	plain, err := s.Store.Keys.Open(raw, pipeline.EncryptionIdentity(ws, id, kind))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, value)
}

// legacyKeys open values with the installation keyring, without a record, for the agent
// delivery path alone.
type legacyKeys struct {
	s  *Service
	ws uuid.UUID
}

func (s *Service) legacyRunKeys(ws uuid.UUID) runKeys {
	return runKeys{operate: legacyKeys{s, ws}, inspect: legacyKeys{s, ws}}
}

func (k legacyKeys) open(item secretbroker.Item, cipher []byte) ([]byte, error) {
	if item.Kind == secretbroker.SecretVersion {
		return k.s.Store.Keys.Open(cipher, store.SecretIdentity(k.ws, item.ID))
	}
	return k.s.Store.Keys.Open(cipher, pipeline.EncryptionIdentity(k.ws, item.ID, string(item.Kind)))
}

func (k legacyKeys) openBetweenPasses(_ context.Context, item secretbroker.Item, cipher []byte) ([]byte, error) {
	return k.open(item, cipher)
}

func (legacyKeys) close(context.Context) {}
